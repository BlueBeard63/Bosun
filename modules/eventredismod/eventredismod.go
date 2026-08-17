// Package eventredismod is a Redis Streams driver for eventmod. Publishing does
// XADD; each subscription reads through a consumer group with XREADGROUP and
// acknowledges with XACK. A named group (WithGroup) makes members compete; a
// subscription with no group gets a private, ephemeral group so it receives
// every message (fan-out). A handler error re-adds the message with an
// incremented attempt (at-least-once redelivery).
//
//	import _ "github.com/amberstack/bosun/modules/eventredismod"
//	var _ = eventredismod.Use()
//
// Redis stream keys are exact, so this driver does not support subject
// wildcards; publish and subscribe on the same literal subject.
package eventredismod

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/amberstack/bosun"
	"github.com/amberstack/bosun/modules/eventmod"
	"github.com/redis/go-redis/v9"
)

// Options configures the Redis connection.
type Options struct {
	Addr     string
	Password string
	DB       int
}

var _ = bosun.Default[*Options](func() *Options { return &Options{Addr: "localhost:6379"} })

// Bus is the Redis-Streams-backed eventmod.Bus.
type Bus struct {
	Opts *Options // injected

	rdb    *redis.Client
	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	subs   map[uint64]*subscription
	nextID uint64
	closed bool
	wg     sync.WaitGroup
}

var _ eventmod.Bus = (*Bus)(nil)

// Init opens the client and verifies connectivity.
func (b *Bus) Init() error {
	b.rdb = redis.NewClient(&redis.Options{Addr: b.Opts.Addr, Password: b.Opts.Password, DB: b.Opts.DB})
	b.ctx, b.cancel = context.WithCancel(context.Background())
	b.subs = map[uint64]*subscription{}
	return b.rdb.Ping(b.ctx).Err()
}

// Close stops every reader, tears down ephemeral groups, and closes the client.
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
		if s.ephemeral {
			_ = b.rdb.XGroupDestroy(context.Background(), s.subject, s.group).Err()
		}
	}
	if b.cancel != nil {
		b.cancel() // unblock any in-flight XREADGROUP
	}
	b.wg.Wait()
	if b.rdb != nil {
		return b.rdb.Close()
	}
	return nil
}

// Publish appends the payload to the subject stream.
func (b *Bus) Publish(ctx context.Context, subject string, data []byte, opts ...eventmod.PubOption) error {
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return eventmod.ErrClosed
	}
	cfg := eventmod.ResolvePub(opts)
	return b.rdb.XAdd(ctx, &redis.XAddArgs{Stream: subject, Values: encode(data, cfg.Headers, 1)}).Err()
}

func encode(data []byte, headers map[string]string, attempt int) map[string]any {
	hdr, _ := json.Marshal(headers)
	return map[string]any{"data": data, "hdr": string(hdr), "attempt": strconv.Itoa(attempt)}
}

func decode(subject string, m redis.XMessage) eventmod.Message {
	var data []byte
	if s, ok := m.Values["data"].(string); ok {
		data = []byte(s)
	}
	hdr := map[string]string{}
	if s, ok := m.Values["hdr"].(string); ok && s != "" {
		_ = json.Unmarshal([]byte(s), &hdr)
	}
	attempt := 1
	if s, ok := m.Values["attempt"].(string); ok {
		if n, err := strconv.Atoi(s); err == nil {
			attempt = n
		}
	}
	return eventmod.Message{Subject: subject, Data: data, Headers: hdr, ID: m.ID, Attempt: attempt}
}

type subscription struct {
	id        uint64
	subject   string
	group     string
	consumer  string
	ephemeral bool
	quit      chan struct{}
	stopOnce  sync.Once
	bus       *Bus
}

func (s *subscription) stop() { s.stopOnce.Do(func() { close(s.quit) }) }

// Subscribe reads the subject stream through a consumer group.
func (b *Bus) Subscribe(ctx context.Context, subject string, h eventmod.Handler, opts ...eventmod.SubOption) (eventmod.Subscription, error) {
	cfg := eventmod.ResolveSub(opts)
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, eventmod.ErrClosed
	}
	id := b.nextID
	b.nextID++
	group := cfg.Group
	ephemeral := false
	if group == "" {
		group = "bosun-" + strconv.FormatUint(id, 10)
		ephemeral = true
	}
	s := &subscription{
		id: id, subject: subject, group: group, ephemeral: ephemeral,
		consumer: "c-" + strconv.FormatUint(id, 10), quit: make(chan struct{}), bus: b,
	}
	b.subs[id] = s
	b.mu.Unlock()

	// "$" starts the group at new messages only, matching in-memory fan-out.
	if err := b.rdb.XGroupCreateMkStream(b.ctx, subject, group, "$").Err(); err != nil && !isBusyGroup(err) {
		return nil, err
	}
	b.wg.Add(1)
	go s.run(h)
	return &subHandle{bus: b, sub: s}, nil
}

func (s *subscription) run(h eventmod.Handler) {
	defer s.bus.wg.Done()
	for {
		select {
		case <-s.quit:
			return
		default:
		}
		res, err := s.bus.rdb.XReadGroup(s.bus.ctx, &redis.XReadGroupArgs{
			Group: s.group, Consumer: s.consumer, Streams: []string{s.subject, ">"},
			Count: 16, Block: time.Second,
		}).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				continue // block timeout, no messages
			}
			if s.bus.ctx.Err() != nil {
				return // bus closing
			}
			select {
			case <-s.quit:
				return
			case <-time.After(200 * time.Millisecond):
			}
			continue
		}
		for _, stream := range res {
			for _, m := range stream.Messages {
				d := &delivery{bus: s.bus, subject: s.subject, group: s.group, id: m.ID, msg: decode(s.subject, m)}
				_ = h(context.Background(), d)
			}
		}
	}
}

type delivery struct {
	bus     *Bus
	subject string
	group   string
	id      string
	msg     eventmod.Message
	once    sync.Once
}

func (d *delivery) Message() eventmod.Message { return d.msg }

func (d *delivery) Ack(ctx context.Context) error {
	d.once.Do(func() { _ = d.bus.rdb.XAck(d.bus.ctx, d.subject, d.group, d.id).Err() })
	return nil
}

func (d *delivery) Nack(ctx context.Context, requeue bool) error {
	d.once.Do(func() {
		if requeue {
			_ = d.bus.rdb.XAdd(d.bus.ctx, &redis.XAddArgs{
				Stream: d.subject, Values: encode(d.msg.Data, d.msg.Headers, d.msg.Attempt+1),
			}).Err()
		}
		_ = d.bus.rdb.XAck(d.bus.ctx, d.subject, d.group, d.id).Err()
	})
	return nil
}

type subHandle struct {
	bus *Bus
	sub *subscription
}

func (h *subHandle) Unsubscribe() error {
	h.bus.mu.Lock()
	delete(h.bus.subs, h.sub.id)
	h.bus.mu.Unlock()
	h.sub.stop()
	if h.sub.ephemeral {
		_ = h.bus.rdb.XGroupDestroy(context.Background(), h.sub.subject, h.sub.group).Err()
	}
	return nil
}

func isBusyGroup(err error) bool {
	return err != nil && strings.Contains(err.Error(), "BUSYGROUP")
}

// Use binds the eventmod interfaces to the Redis bus (host registrations win).
func Use() struct{} {
	bosun.Service[Bus]()
	bosun.DefaultBind[eventmod.Bus, Bus]()
	bosun.DefaultBind[eventmod.Publisher, Bus]()
	bosun.DefaultBind[eventmod.Subscriber, Bus]()
	return struct{}{}
}
