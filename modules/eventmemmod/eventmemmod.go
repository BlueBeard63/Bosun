// Package eventmemmod is the in-memory default driver for eventmod. It requires
// no broker and is bound automatically (via bosun.DefaultBind) when no other
// event driver is imported, so tests and single-process apps get a working bus
// for free. It is the reference implementation the broker drivers match:
// at-least-once delivery, Nack-requeue redelivery, named consumer groups
// (competing consumers), and graceful drain on shutdown.
//
//	import _ "github.com/amberstack/bosun/modules/eventmemmod"
//	var _ = eventmemmod.Default()
//
// Messages live only in memory: anything queued-but-undelivered is lost on
// shutdown. Durability requires a broker driver.
package eventmemmod

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/amberstack/bosun"
	"github.com/amberstack/bosun/modules/eventmod"
)

// Bus is the in-memory eventmod.Bus. The zero value is usable after Init (called
// automatically by the DI container) or via New.
type Bus struct {
	mu     sync.Mutex
	subs   []*subscription
	rr     map[string]int // group key -> round-robin cursor
	closed bool
	nextID uint64
}

var _ eventmod.Bus = (*Bus)(nil)

// New returns a ready in-memory bus (for tests and manual wiring).
func New() *Bus { return &Bus{rr: map[string]int{}} }

// Init satisfies the DI lifecycle; it prepares internal state.
func (b *Bus) Init() error {
	b.mu.Lock()
	b.ensureLocked()
	b.mu.Unlock()
	return nil
}

func (b *Bus) ensureLocked() {
	if b.rr == nil {
		b.rr = map[string]int{}
	}
}

var idCounter atomic.Uint64

func newID() string { return "mem-" + strconv.FormatUint(idCounter.Add(1), 10) }

// --- publish ---

// Publish delivers data to every matching subscription. Subscriptions in the
// same consumer group (same subject + group) share messages round-robin;
// subscriptions with no group (or in different groups) each get a copy. A
// publish with no matching subscriber is a no-op (returns nil).
func (b *Bus) Publish(ctx context.Context, subject string, data []byte, opts ...eventmod.PubOption) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg := eventmod.ResolvePub(opts)
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return eventmod.ErrClosed
	}
	b.ensureLocked()
	targets := b.selectTargetsLocked(subject)
	b.mu.Unlock()

	for _, s := range targets {
		m := eventmod.Message{Subject: subject, Data: data, ID: newID(), Attempt: 1}
		if len(cfg.Headers) > 0 {
			m.Headers = cloneHeaders(cfg.Headers)
		}
		s.enqueue(m)
	}
	return nil
}

func cloneHeaders(h map[string]string) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		out[k] = v
	}
	return out
}

// selectTargetsLocked returns exactly one subscription per matched group.
func (b *Bus) selectTargetsLocked(subject string) []*subscription {
	tokens := strings.Split(subject, ".")
	groups := map[string][]*subscription{}
	var order []string
	for _, s := range b.subs {
		if !matchTokens(s.tokens, tokens) {
			continue
		}
		if _, ok := groups[s.groupKey]; !ok {
			order = append(order, s.groupKey)
		}
		groups[s.groupKey] = append(groups[s.groupKey], s)
	}
	out := make([]*subscription, 0, len(order))
	for _, gk := range order {
		members := groups[gk]
		if len(members) == 1 {
			out = append(out, members[0])
			continue
		}
		i := b.rr[gk] % len(members)
		b.rr[gk] = (b.rr[gk] + 1) & 0x3fffffff
		out = append(out, members[i])
	}
	return out
}

// matchTokens reports whether a subscription pattern matches a subject. Tokens
// split on '.'; '*' matches one token and '>' matches the remaining tokens
// (NATS-style). Broker drivers translate to their own wildcard grammar.
func matchTokens(pattern, subject []string) bool {
	for i, p := range pattern {
		if p == ">" {
			return true
		}
		if i >= len(subject) {
			return false
		}
		if p != "*" && p != subject[i] {
			return false
		}
	}
	return len(pattern) == len(subject)
}

// --- subscribe ---

type subscription struct {
	id       uint64
	subject  string
	tokens   []string
	group    string
	groupKey string
	handler  eventmod.Handler
	ch       chan eventmod.Message
	quit     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
	bus      *Bus
}

// Subscribe registers h for subject. With WithGroup, subscriptions sharing the
// group compete for messages; without it, the subscription receives every
// matching message. WithMaxInFlight sets the number of concurrent workers.
func (b *Bus) Subscribe(ctx context.Context, subject string, h eventmod.Handler, opts ...eventmod.SubOption) (eventmod.Subscription, error) {
	cfg := eventmod.ResolveSub(opts)
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, eventmod.ErrClosed
	}
	b.ensureLocked()
	id := b.nextID
	b.nextID++
	s := &subscription{
		id:      id,
		subject: subject,
		tokens:  strings.Split(subject, "."),
		group:   cfg.Group,
		handler: h,
		ch:      make(chan eventmod.Message, 1024),
		quit:    make(chan struct{}),
		bus:     b,
	}
	if cfg.Group == "" {
		s.groupKey = "s:" + strconv.FormatUint(id, 10)
	} else {
		s.groupKey = "g:" + subject + "\x00" + cfg.Group
	}
	b.subs = append(b.subs, s)
	b.mu.Unlock()

	workers := max(cfg.MaxInFlight, 1)
	for i := 0; i < workers; i++ {
		s.wg.Add(1)
		go s.run()
	}
	return &subHandle{bus: b, sub: s}, nil
}

func (s *subscription) run() {
	defer s.wg.Done()
	for {
		select {
		case <-s.quit:
			return
		case m := <-s.ch:
			d := &delivery{sub: s, msg: m}
			_ = s.handler(context.Background(), d)
		}
	}
}

// enqueue hands a message to the subscription without ever blocking a caller:
// if the buffer is full it retries in a goroutine that respects quit.
func (s *subscription) enqueue(m eventmod.Message) {
	select {
	case s.ch <- m:
	case <-s.quit:
	default:
		go func() {
			select {
			case s.ch <- m:
			case <-s.quit:
			}
		}()
	}
}

func (s *subscription) stop() {
	s.stopOnce.Do(func() { close(s.quit) })
	s.wg.Wait()
}

type subHandle struct {
	bus *Bus
	sub *subscription
}

func (h *subHandle) Unsubscribe() error {
	h.bus.removeSub(h.sub)
	return nil
}

func (b *Bus) removeSub(s *subscription) {
	b.mu.Lock()
	for i, x := range b.subs {
		if x == s {
			b.subs = append(b.subs[:i], b.subs[i+1:]...)
			break
		}
	}
	b.mu.Unlock()
	s.stop()
}

// --- delivery ---

type delivery struct {
	sub  *subscription
	msg  eventmod.Message
	once sync.Once
}

func (d *delivery) Message() eventmod.Message { return d.msg }

func (d *delivery) Ack(ctx context.Context) error {
	d.once.Do(func() {})
	return nil
}

func (d *delivery) Nack(ctx context.Context, requeue bool) error {
	d.once.Do(func() {
		if requeue {
			m := d.msg
			m.Attempt++
			d.sub.enqueue(m)
		}
	})
	return nil
}

// --- lifecycle ---

// Close drains: it stops accepting work and waits for in-flight handlers to
// finish. Implements io.Closer so registry.Shutdown closes it automatically.
func (b *Bus) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	subs := b.subs
	b.subs = nil
	b.mu.Unlock()
	for _, s := range subs {
		s.stop()
	}
	return nil
}

// --- registration ---

// Default registers the in-memory bus as the default eventmod backend. Import
// the package and call it at init:
//
//	var _ = eventmemmod.Default()
func Default() struct{} {
	bosun.Service[Bus]()
	bosun.DefaultBind[eventmod.Bus, Bus]()
	bosun.DefaultBind[eventmod.Publisher, Bus]()
	bosun.DefaultBind[eventmod.Subscriber, Bus]()
	return struct{}{}
}
