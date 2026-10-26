// Package eventnatsmod is a NATS driver for eventmod. It binds the eventmod
// interfaces to a NATS connection so publishing and subscribing go over a NATS
// server instead of the in-memory bus.
//
//	import _ "github.com/bluebeard63/bosun/modules/eventnatsmod"
//	var _ = eventnatsmod.Use()
//
// Consumer groups map to NATS queue groups (competing consumers). Core NATS is
// fire-and-forget, so a Nack with requeue re-publishes the message, with an
// incremented attempt, to a retry subject that only the failing subscription
// (or the members of its group) listens on; other subscribers of the subject
// never see it twice. This redelivery is best-effort: a message in flight when
// the process dies, or a retry with no member left to receive it, is lost. For
// durable delivery back the subject with a JetStream stream. NATS subject
// wildcards (`*`, `>`) match eventmod's grammar directly.
package eventnatsmod

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"sync"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/modules/eventmod"
	"github.com/nats-io/nats.go"
)

const (
	hdrAttempt = "Bosun-Attempt"
	// hdrSubject carries the original subject on a retry copy.
	hdrSubject  = "Bosun-Subject"
	retryPrefix = "_BOSUN.RETRY."
)

// Options configures the NATS connection.
type Options struct {
	URL string
}

var _ = bosun.Default[*Options](func() *Options { return &Options{URL: nats.DefaultURL} })

// Bus is the NATS-backed eventmod.Bus.
type Bus struct {
	Opts *Options // injected

	mu     sync.Mutex
	nc     *nats.Conn
	subs   []*nats.Subscription
	closed bool
}

var _ eventmod.Bus = (*Bus)(nil)

// Init dials the NATS server.
func (b *Bus) Init() error {
	nc, err := nats.Connect(b.Opts.URL)
	if err != nil {
		return err
	}
	b.nc = nc
	return nil
}

// Close unsubscribes and drops the connection.
func (b *Bus) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	subs := b.subs
	b.subs = nil
	nc := b.nc
	b.mu.Unlock()
	for _, s := range subs {
		_ = s.Unsubscribe()
	}
	if nc != nil {
		nc.Close()
	}
	return nil
}

// Publish sends data to a subject, carrying eventmod headers as NATS headers.
func (b *Bus) Publish(ctx context.Context, subject string, data []byte, opts ...eventmod.PubOption) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg := eventmod.ResolvePub(opts)
	msg := nats.NewMsg(subject)
	msg.Data = data
	for k, v := range cfg.Headers {
		msg.Header.Set(k, v)
	}
	if msg.Header.Get(hdrAttempt) == "" {
		msg.Header.Set(hdrAttempt, "1")
	}
	return b.nc.PublishMsg(msg)
}

// Subscribe registers a handler; a group makes members compete via a NATS queue
// group. The subscription also listens on its retry subject (see retrySubject),
// where Nack with requeue sends the message back.
func (b *Bus) Subscribe(ctx context.Context, subject string, h eventmod.Handler, opts ...eventmod.SubOption) (eventmod.Subscription, error) {
	cfg := eventmod.ResolveSub(opts)
	retry := retrySubject(subject, cfg.Group)
	cb := func(m *nats.Msg) {
		d := &delivery{bus: b, retry: retry, msg: toMessage(m)}
		_ = h(context.Background(), d)
	}
	sub, err := b.subscribe(subject, cfg.Group, cb)
	if err != nil {
		return nil, err
	}
	rsub, err := b.subscribe(retry, cfg.Group, cb)
	if err != nil {
		_ = sub.Unsubscribe()
		return nil, err
	}
	b.mu.Lock()
	b.subs = append(b.subs, sub, rsub)
	b.mu.Unlock()
	return &subHandle{subs: []*nats.Subscription{sub, rsub}}, nil
}

func (b *Bus) subscribe(subject, group string, cb nats.MsgHandler) (*nats.Subscription, error) {
	if group != "" {
		return b.nc.QueueSubscribe(subject, group, cb)
	}
	return b.nc.Subscribe(subject, cb)
}

// retrySubject is where a subscription's requeued messages go. Members of a
// group, in any process, derive the same subject from the group and subject
// and share it through the queue group; an ungrouped subscription gets a
// unique one of its own.
func retrySubject(subject, group string) string {
	if group == "" {
		return retryPrefix + nats.NewInbox()
	}
	sum := sha256.Sum256([]byte(group + "\x00" + subject))
	return retryPrefix + hex.EncodeToString(sum[:16])
}

func toMessage(m *nats.Msg) eventmod.Message {
	hdr := make(map[string]string, len(m.Header))
	for k := range m.Header {
		hdr[k] = m.Header.Get(k)
	}
	attempt := 1
	if a := m.Header.Get(hdrAttempt); a != "" {
		if n, err := strconv.Atoi(a); err == nil {
			attempt = n
		}
	}
	subject := m.Subject
	if s := m.Header.Get(hdrSubject); s != "" {
		subject = s
	}
	return eventmod.Message{Subject: subject, Data: m.Data, Headers: hdr, ID: m.Reply, Attempt: attempt}
}

type delivery struct {
	bus   *Bus
	retry string // the subscription's retry subject
	msg   eventmod.Message
	once  sync.Once
}

func (d *delivery) Message() eventmod.Message { return d.msg }

func (d *delivery) Ack(ctx context.Context) error {
	d.once.Do(func() {})
	return nil
}

// Nack with requeue publishes a copy with an incremented attempt to the
// subscription's retry subject and returns any publish error.
func (d *delivery) Nack(ctx context.Context, requeue bool) error {
	var err error
	d.once.Do(func() {
		if !requeue {
			return
		}
		if err = ctx.Err(); err != nil {
			return
		}
		msg := nats.NewMsg(d.retry)
		msg.Data = d.msg.Data
		for k, v := range d.msg.Headers {
			msg.Header.Set(k, v)
		}
		msg.Header.Set(hdrAttempt, strconv.Itoa(d.msg.Attempt+1))
		msg.Header.Set(hdrSubject, d.msg.Subject)
		err = d.bus.nc.PublishMsg(msg)
	})
	return err
}

type subHandle struct{ subs []*nats.Subscription }

func (h *subHandle) Unsubscribe() error {
	var first error
	for _, s := range h.subs {
		if err := s.Unsubscribe(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Use binds the eventmod interfaces to the NATS bus (host registrations win).
func Use() struct{} {
	bosun.Service[Bus]()
	bosun.DefaultBind[eventmod.Bus, Bus]()
	bosun.DefaultBind[eventmod.Publisher, Bus]()
	bosun.DefaultBind[eventmod.Subscriber, Bus]()
	bosun.DefaultBind[eventmod.Requester, Bus]()
	bosun.DefaultBind[eventmod.Responder, Bus]()
	return struct{}{}
}
