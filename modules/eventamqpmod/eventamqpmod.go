// Package eventamqpmod is a RabbitMQ (AMQP 0-9-1) driver for eventmod. Messages
// are published to a topic exchange keyed by subject; each subscription binds a
// queue to that exchange. A named group (WithGroup) binds a shared durable
// queue named "<group>.<subject>" so members compete; a subscription with no
// group gets an exclusive auto-delete queue so it receives every message
// (fan-out). Delivery is manual ack; a handler error re-publishes with an
// incremented attempt (at-least-once). Publish waits for a publisher confirm,
// so it returns nil only once the broker has taken the message.
//
//	import _ "github.com/bluebeard63/bosun/modules/eventamqpmod"
//	var _ = eventamqpmod.Use()
//
// eventmod's `>` tail wildcard is translated to AMQP's `#`; `*` is the same in
// both grammars.
package eventamqpmod

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/modules/eventmod"
	amqp "github.com/rabbitmq/amqp091-go"
)

const hdrAttempt = "Bosun-Attempt"

var (
	// ErrNacked is returned by Publish when the broker rejects the message, or
	// the channel closes before confirming it.
	ErrNacked = errors.New("eventamqpmod: publish not confirmed by broker")
	// ErrUnroutable is returned by Publish when Options.Mandatory is set and no
	// queue is bound to the subject.
	ErrUnroutable = errors.New("eventamqpmod: message unroutable")
)

// Options configures the AMQP connection and exchange.
type Options struct {
	URL      string
	Exchange string
	// Kind is the exchange type: "topic" (default, wildcard routing), "direct"
	// (exact routing), or "fanout" (every bound queue receives everything).
	Kind     string
	Prefetch int
	// Mandatory makes Publish fail with ErrUnroutable when no queue is bound to
	// the subject, instead of the broker silently dropping the message.
	Mandatory bool
}

var _ = bosun.Default[*Options](func() *Options {
	return &Options{URL: "amqp://guest:guest@localhost:5672/", Exchange: "bosun.events", Kind: "topic", Prefetch: 32}
})

func (o *Options) kind() string {
	if o.Kind == "" {
		return "topic"
	}
	return o.Kind
}

// Bus is the AMQP-backed eventmod.Bus.
type Bus struct {
	Opts *Options // injected

	mu      sync.Mutex
	conn    *amqp.Connection
	pubCh   *amqp.Channel
	returns chan amqp.Return // basic.return frames for pubCh (mandatory publishes)
	pubMu   sync.Mutex       // amqp channels are not safe for concurrent publish
	subs    []*subscription
	closed  bool
	closeCh chan struct{} // closed on Close, to stop the reconnect supervisor
}

var _ eventmod.Bus = (*Bus)(nil)

// Init dials the broker, opens a publish channel, declares the exchange, and
// starts a supervisor that reconnects if the connection later drops.
func (b *Bus) Init() error {
	b.closeCh = make(chan struct{})
	return b.connect()
}

// connect dials the broker, (re)opens the publish channel in confirm mode +
// exchange, and starts a supervisor watching this connection for an unexpected
// close. Called once at Init and again on every reconnect.
func (b *Bus) connect() error {
	conn, err := amqp.Dial(b.Opts.URL)
	if err != nil {
		return err
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return err
	}
	if err := ch.ExchangeDeclare(b.Opts.Exchange, b.Opts.kind(), true, false, false, false, nil); err != nil {
		_ = conn.Close()
		return err
	}
	if err := ch.Confirm(false); err != nil {
		_ = conn.Close()
		return err
	}
	// Buffered so the connection's reader never blocks on a return nobody has
	// collected yet (e.g. after a publish stopped waiting on its ctx).
	returns := ch.NotifyReturn(make(chan amqp.Return, 16))
	b.mu.Lock()
	b.conn = conn
	b.pubCh = ch
	b.returns = returns
	b.mu.Unlock()
	go b.supervise(conn)
	return nil
}

// supervise blocks until conn closes. On an UNEXPECTED close (not Bus.Close), it
// redials with backoff and re-establishes the publish channel + every
// subscription — so a broker blip doesn't permanently kill publishers or silently
// leave consumers dead. Without this, the consumer goroutines' `range deliveries`
// simply ends when the channel drops and never resumes.
func (b *Bus) supervise(conn *amqp.Connection) {
	errCh := conn.NotifyClose(make(chan *amqp.Error, 1))
	select {
	case <-b.closeCh:
		return
	case reason := <-errCh:
		if b.isClosed() {
			return
		}
		log.Printf("eventamqpmod: broker connection lost (%v) — reconnecting", reason)
	}
	for attempt := 0; ; attempt++ {
		select {
		case <-b.closeCh:
			return
		case <-time.After(backoff(attempt)):
		}
		if err := b.connect(); err != nil { // connect() starts a fresh supervisor on success
			continue
		}
		b.reestablish()
		log.Printf("eventamqpmod: reconnected to broker; subscriptions re-established")
		return
	}
}

// reestablish re-creates every recorded subscription on the current connection.
func (b *Bus) reestablish() {
	b.mu.Lock()
	subs := make([]*subscription, len(b.subs))
	copy(subs, b.subs)
	b.mu.Unlock()
	for _, s := range subs {
		if err := b.establish(s); err != nil {
			log.Printf("eventamqpmod: re-subscribe %q failed: %v (will retry on next drop)", s.subject, err)
		}
	}
}

func (b *Bus) isClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

// backoff caps the redial delay at ~3.2s (200ms, 400ms, … doubling).
func backoff(attempt int) time.Duration {
	if attempt > 4 {
		attempt = 4
	}
	return (200 * time.Millisecond) << attempt
}

// Close tears down consumer channels and the connection.
func (b *Bus) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	if b.closeCh != nil {
		close(b.closeCh) // stop the reconnect supervisor
	}
	subs := b.subs
	b.subs = nil
	conn := b.conn
	pubCh := b.pubCh
	b.mu.Unlock()

	for _, s := range subs {
		if s.ch != nil {
			_ = s.ch.Close() // ends the delivery range, stopping the goroutine
		}
	}
	if pubCh != nil {
		_ = pubCh.Close()
	}
	if conn != nil {
		return conn.Close()
	}
	return nil
}

// Publish routes data to the exchange with the subject as the routing key and
// waits for the broker to confirm it.
func (b *Bus) Publish(ctx context.Context, subject string, data []byte, opts ...eventmod.PubOption) error {
	cfg := eventmod.ResolvePub(opts)
	headers := amqp.Table{}
	for k, v := range cfg.Headers {
		headers[k] = v
	}
	if _, ok := headers[hdrAttempt]; !ok {
		headers[hdrAttempt] = "1"
	}
	return b.publish(ctx, b.Opts.Exchange, subject, b.Opts.Mandatory, amqp.Publishing{
		ContentType: "application/octet-stream",
		Body:        data,
		Headers:     headers,
	})
}

// publish sends msg on the shared publish channel and blocks until the broker
// confirms it. With mandatory set, a message no queue accepts comes back as a
// basic.return, which the broker sends before the confirm.
func (b *Bus) publish(ctx context.Context, exchange, key string, mandatory bool, msg amqp.Publishing) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	closed := b.closed
	ch := b.pubCh
	returns := b.returns
	b.mu.Unlock()
	if closed {
		return eventmod.ErrClosed
	}
	b.pubMu.Lock()
	defer b.pubMu.Unlock()
	drainReturns(returns) // left over from a publish that stopped waiting early
	dc, err := ch.PublishWithDeferredConfirmWithContext(ctx, exchange, key, mandatory, false, msg)
	if err != nil {
		return err
	}
	ack, err := dc.WaitContext(ctx)
	if err != nil {
		return err
	}
	if !ack {
		return fmt.Errorf("%w (routing key %q)", ErrNacked, key)
	}
	select {
	case r, ok := <-returns:
		if ok {
			return fmt.Errorf("%w (routing key %q): %s", ErrUnroutable, key, r.ReplyText)
		}
	default:
	}
	return nil
}

func drainReturns(c chan amqp.Return) {
	for {
		select {
		case <-c:
		default:
			return
		}
	}
}

type subscription struct {
	bus     *Bus
	subject string
	handler eventmod.Handler        // set by Subscribe
	respond eventmod.RequestHandler // set by Respond
	opts    []eventmod.SubOption
	ch      *amqp.Channel // current consumer channel; replaced on reconnect
}

// Subscribe records the subscription and establishes it on the current
// connection. The recorded params let the supervisor re-establish it verbatim
// after a reconnect.
func (b *Bus) Subscribe(ctx context.Context, subject string, h eventmod.Handler, opts ...eventmod.SubOption) (eventmod.Subscription, error) {
	return b.subscribe(&subscription{bus: b, subject: subject, handler: h, opts: opts})
}

func (b *Bus) subscribe(s *subscription) (eventmod.Subscription, error) {
	if b.isClosed() {
		return nil, eventmod.ErrClosed
	}
	if err := b.establish(s); err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.subs = append(b.subs, s)
	b.mu.Unlock()
	return &subHandle{bus: b, sub: s}, nil
}

// establish (re)opens s's channel, declares + binds its queue, starts consuming,
// and runs the delivery loop. Idempotent per call — used by Subscribe/Respond and
// by the supervisor on reconnect. The delivery goroutine ends when the channel
// drops; a reconnect calls establish again to start a fresh one.
func (b *Bus) establish(s *subscription) error {
	b.mu.Lock()
	conn := b.conn
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return eventmod.ErrClosed
	}
	cfg := eventmod.ResolveSub(s.opts)

	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	if b.Opts.Prefetch > 0 {
		if err := ch.Qos(b.Opts.Prefetch, 0, false); err != nil {
			_ = ch.Close()
			return err
		}
	}

	var qname string
	if cfg.Group != "" {
		// One queue per group AND subject: a group consuming two subjects must
		// not share a queue, or each handler would get the other's messages.
		q, err := ch.QueueDeclare(cfg.Group+"."+s.subject, true, false, false, false, nil) // durable, shared
		if err != nil {
			_ = ch.Close()
			return err
		}
		qname = q.Name
	} else {
		q, err := ch.QueueDeclare("", false, true, true, false, nil) // exclusive, auto-delete
		if err != nil {
			_ = ch.Close()
			return err
		}
		qname = q.Name
	}
	if err := ch.QueueBind(qname, translateKey(s.subject), b.Opts.Exchange, false, nil); err != nil {
		_ = ch.Close()
		return err
	}
	deliveries, err := ch.Consume(qname, "", false, false, false, false, nil) // manual ack
	if err != nil {
		_ = ch.Close()
		return err
	}

	b.mu.Lock()
	s.ch = ch
	b.mu.Unlock()

	if s.respond != nil {
		go s.serveRequests(ch, deliveries)
		return nil
	}
	go func() {
		for m := range deliveries {
			d := &delivery{bus: b, subject: s.subject, amqpDel: m, msg: toMessage(m)}
			_ = s.handler(context.Background(), d)
		}
	}()
	return nil
}

// toMessage converts a delivery. Subject is the routing key the message was
// published with, not the (possibly wildcard) subscription pattern.
func toMessage(m amqp.Delivery) eventmod.Message {
	hdr := make(map[string]string, len(m.Headers))
	for k, v := range m.Headers {
		if s, ok := v.(string); ok {
			hdr[k] = s
		}
	}
	attempt := 1
	if s := hdr[hdrAttempt]; s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			attempt = n
		}
	}
	return eventmod.Message{Subject: m.RoutingKey, Data: m.Body, Headers: hdr, ID: m.MessageId, Attempt: attempt}
}

type delivery struct {
	bus     *Bus
	subject string
	amqpDel amqp.Delivery
	msg     eventmod.Message
	once    sync.Once
}

func (d *delivery) Message() eventmod.Message { return d.msg }

func (d *delivery) Ack(ctx context.Context) error {
	d.once.Do(func() { _ = d.amqpDel.Ack(false) })
	return nil
}

func (d *delivery) Nack(ctx context.Context, requeue bool) error {
	d.once.Do(func() {
		if requeue {
			hdr := make(map[string]string, len(d.msg.Headers)+1)
			for k, v := range d.msg.Headers {
				hdr[k] = v
			}
			hdr[hdrAttempt] = strconv.Itoa(d.msg.Attempt + 1)
			_ = d.bus.Publish(ctx, d.subject, d.msg.Data, eventmod.WithHeaders(hdr))
		}
		_ = d.amqpDel.Ack(false)
	})
	return nil
}

type subHandle struct {
	bus *Bus
	sub *subscription
}

func (h *subHandle) Unsubscribe() error {
	h.bus.mu.Lock()
	for i, s := range h.bus.subs {
		if s == h.sub {
			h.bus.subs = append(h.bus.subs[:i], h.bus.subs[i+1:]...)
			break
		}
	}
	ch := h.sub.ch
	h.bus.mu.Unlock()
	if ch != nil {
		return ch.Close()
	}
	return nil
}

// translateKey converts an eventmod subject into an AMQP topic routing key: the
// tail wildcard `>` becomes `#`; `*` is unchanged.
func translateKey(subject string) string {
	parts := strings.Split(subject, ".")
	for i, p := range parts {
		if p == ">" {
			parts[i] = "#"
		}
	}
	return strings.Join(parts, ".")
}

// Use binds the eventmod interfaces to the AMQP bus (host registrations win).
func Use() struct{} {
	bosun.Service[Bus]()
	bosun.DefaultBind[eventmod.Bus, Bus]()
	bosun.DefaultBind[eventmod.Publisher, Bus]()
	bosun.DefaultBind[eventmod.Subscriber, Bus]()
	bosun.DefaultBind[eventmod.Requester, Bus]()
	bosun.DefaultBind[eventmod.Responder, Bus]()
	return struct{}{}
}
