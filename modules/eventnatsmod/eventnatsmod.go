// Package eventnatsmod is a NATS driver for eventmod. It binds the eventmod
// interfaces to a NATS connection so publishing and subscribing go over a NATS
// server instead of the in-memory bus.
//
//	import _ "github.com/amberstack/bosun/modules/eventnatsmod"
//	var _ = eventnatsmod.Use()
//
// Consumer groups map to NATS queue groups (competing consumers). Core NATS is
// fire-and-forget, so a handler error re-publishes the message with an
// incremented attempt to approximate at-least-once redelivery; for durable
// delivery back it with a JetStream stream. NATS subject wildcards (`*`, `>`)
// match eventmod's grammar directly.
package eventnatsmod

import (
	"context"
	"strconv"
	"sync"

	"github.com/amberstack/bosun"
	"github.com/amberstack/bosun/modules/eventmod"
	"github.com/nats-io/nats.go"
)

const hdrAttempt = "Bosun-Attempt"

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

// Subscribe registers a handler; a group makes members compete via a NATS queue group.
func (b *Bus) Subscribe(ctx context.Context, subject string, h eventmod.Handler, opts ...eventmod.SubOption) (eventmod.Subscription, error) {
	cfg := eventmod.ResolveSub(opts)
	cb := func(m *nats.Msg) {
		d := &delivery{bus: b, subject: subject, msg: toMessage(m)}
		_ = h(context.Background(), d)
	}
	var (
		sub *nats.Subscription
		err error
	)
	if cfg.Group != "" {
		sub, err = b.nc.QueueSubscribe(subject, cfg.Group, cb)
	} else {
		sub, err = b.nc.Subscribe(subject, cb)
	}
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.subs = append(b.subs, sub)
	b.mu.Unlock()
	return &subHandle{sub: sub}, nil
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
	return eventmod.Message{Subject: m.Subject, Data: m.Data, Headers: hdr, ID: m.Reply, Attempt: attempt}
}

type delivery struct {
	bus     *Bus
	subject string
	msg     eventmod.Message
	once    sync.Once
}

func (d *delivery) Message() eventmod.Message { return d.msg }

func (d *delivery) Ack(ctx context.Context) error {
	d.once.Do(func() {})
	return nil
}

func (d *delivery) Nack(ctx context.Context, requeue bool) error {
	d.once.Do(func() {
		if !requeue {
			return
		}
		hdr := make(map[string]string, len(d.msg.Headers)+1)
		for k, v := range d.msg.Headers {
			hdr[k] = v
		}
		hdr[hdrAttempt] = strconv.Itoa(d.msg.Attempt + 1)
		_ = d.bus.Publish(ctx, d.subject, d.msg.Data, eventmod.WithHeaders(hdr))
	})
	return nil
}

type subHandle struct{ sub *nats.Subscription }

func (h *subHandle) Unsubscribe() error { return h.sub.Unsubscribe() }

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
