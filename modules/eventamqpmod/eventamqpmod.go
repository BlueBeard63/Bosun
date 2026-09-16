// Package eventamqpmod is a RabbitMQ (AMQP 0-9-1) driver for eventmod. Messages
// are published to a topic exchange keyed by subject; each subscription binds a
// queue to that exchange. A named group (WithGroup) binds a shared durable
// queue so members compete; a subscription with no group gets an exclusive
// auto-delete queue so it receives every message (fan-out). Delivery is manual
// ack; a handler error re-publishes with an incremented attempt (at-least-once).
//
//	import _ "github.com/amberstack/bosun/modules/eventamqpmod"
//	var _ = eventamqpmod.Use()
//
// eventmod's `>` tail wildcard is translated to AMQP's `#`; `*` is the same in
// both grammars.
package eventamqpmod

import (
	"context"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/amberstack/bosun"
	"github.com/amberstack/bosun/modules/eventmod"
	amqp "github.com/rabbitmq/amqp091-go"
)

const hdrAttempt = "Bosun-Attempt"

// Options configures the AMQP connection and exchange.
type Options struct {
	URL      string
	Exchange string
	// Kind is the exchange type: "topic" (default, wildcard routing), "direct"
	// (exact routing), or "fanout" (every bound queue receives everything).
	Kind     string
	Prefetch int
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
	pubMu   sync.Mutex    // amqp channels are not safe for concurrent publish
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

// connect dials the broker, (re)opens the publish channel + exchange, and starts
// a supervisor watching this connection for an unexpected close. Called once at
// Init and again on every reconnect.
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
	b.mu.Lock()
	b.conn = conn
	b.pubCh = ch
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

// Publish routes data to the exchange with the subject as the routing key.
func (b *Bus) Publish(ctx context.Context, subject string, data []byte, opts ...eventmod.PubOption) error {
	b.mu.Lock()
	closed := b.closed
	ch := b.pubCh
	ex := b.Opts.Exchange
	b.mu.Unlock()
	if closed {
		return eventmod.ErrClosed
	}
	cfg := eventmod.ResolvePub(opts)
	headers := amqp.Table{}
	for k, v := range cfg.Headers {
		headers[k] = v
	}
	if _, ok := headers[hdrAttempt]; !ok {
		headers[hdrAttempt] = "1"
	}
	b.pubMu.Lock()
	defer b.pubMu.Unlock()
	return ch.PublishWithContext(ctx, ex, subject, false, false, amqp.Publishing{
		ContentType: "application/octet-stream",
		Body:        data,
		Headers:     headers,
	})
}

type subscription struct {
	bus     *Bus
	subject string
	handler eventmod.Handler
	opts    []eventmod.SubOption
	ch      *amqp.Channel // current consumer channel; replaced on reconnect
}

// Subscribe records the subscription and establishes it on the current
// connection. The recorded params let the supervisor re-establish it verbatim
// after a reconnect.
func (b *Bus) Subscribe(ctx context.Context, subject string, h eventmod.Handler, opts ...eventmod.SubOption) (eventmod.Subscription, error) {
	if b.isClosed() {
		return nil, eventmod.ErrClosed
	}
	s := &subscription{bus: b, subject: subject, handler: h, opts: opts}
	if err := b.establish(s); err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.subs = append(b.subs, s)
	b.mu.Unlock()
	return &subHandle{bus: b, sub: s}, nil
}

// establish (re)opens s's channel, declares + binds its queue, starts consuming,
// and runs the delivery loop. Idempotent per call — used by Subscribe and by the
// supervisor on reconnect. The delivery goroutine ends when the channel drops; a
// reconnect calls establish again to start a fresh one.
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
		q, err := ch.QueueDeclare(cfg.Group, true, false, false, false, nil) // durable, shared
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

	go func() {
		for m := range deliveries {
			d := &delivery{bus: b, subject: s.subject, amqpDel: m, msg: toMessage(s.subject, m)}
			_ = s.handler(context.Background(), d)
		}
	}()
	return nil
}

func toMessage(subject string, m amqp.Delivery) eventmod.Message {
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
	return eventmod.Message{Subject: subject, Data: m.Body, Headers: hdr, ID: m.MessageId, Attempt: attempt}
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
