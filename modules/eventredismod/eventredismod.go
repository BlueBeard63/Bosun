// Package eventredismod is a Redis Streams driver for eventmod. Publishing does
// XADD; each subscription reads through a consumer group with XREADGROUP and
// acknowledges with XACK. A named group (WithGroup) makes members compete; a
// subscription with no group gets a private, ephemeral group so it receives
// every message (fan-out). A Nack with requeue leaves the entry pending in the
// subscription's group and claims it back for another attempt (at-least-once
// redelivery); Attempt is the entry's delivery count, and other groups on the
// stream are unaffected. Entries left pending by a consumer that died are
// claimed by another member of the group once idle for Options.ClaimIdle.
//
//	import _ "github.com/bluebeard63/bosun/modules/eventredismod"
//	var _ = eventredismod.Use()
//
// Redis stream keys are exact, so this driver does not support subject
// wildcards; publish and subscribe on the same literal subject.
package eventredismod

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/modules/eventmod"
	"github.com/redis/go-redis/v9"
)

// Options configures the Redis connection.
type Options struct {
	Addr     string
	Password string
	DB       int
	// ClaimIdle is how long an entry may sit unacknowledged with one consumer
	// before another member of the group claims and redelivers it, which is
	// how messages held by a crashed consumer are recovered. A handler that
	// runs longer than this may see its message handled twice. Default 30s.
	ClaimIdle time.Duration
	// MaxLen caps each stream at roughly this many entries (XADD MAXLEN ~).
	// 0 means streams are never trimmed. Entries trimmed before every group
	// has read them are lost to those groups, so size it for the slowest one.
	MaxLen int64
}

const defaultClaimIdle = 30 * time.Second

func (o *Options) claimIdle() time.Duration {
	if o.ClaimIdle <= 0 {
		return defaultClaimIdle
	}
	return o.ClaimIdle
}

var _ = bosun.Default[*Options](func() *Options { return &Options{Addr: "localhost:6379"} })

// Bus is the Redis-Streams-backed eventmod.Bus.
type Bus struct {
	Opts *Options // injected

	rdb      *redis.Client
	ctx      context.Context
	cancel   context.CancelFunc
	instance string // random per Bus; makes group and consumer names unique across replicas

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
	var id [8]byte
	_, _ = rand.Read(id[:])
	b.instance = hex.EncodeToString(id[:])
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
	return b.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: subject, Values: encode(data, cfg.Headers, 1),
		MaxLen: b.Opts.MaxLen, Approx: true,
	}).Err()
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

	retryMu sync.Mutex
	retries []string // pending entry IDs nacked with requeue, redelivered next pass
}

func (s *subscription) stop() { s.stopOnce.Do(func() { close(s.quit) }) }

func (s *subscription) requeue(id string) {
	s.retryMu.Lock()
	s.retries = append(s.retries, id)
	s.retryMu.Unlock()
}

func (s *subscription) takeRetries() []string {
	s.retryMu.Lock()
	defer s.retryMu.Unlock()
	ids := s.retries
	s.retries = nil
	return ids
}

func (s *subscription) hasRetries() bool {
	s.retryMu.Lock()
	defer s.retryMu.Unlock()
	return len(s.retries) > 0
}

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
	// Names include the Bus's random instance id: a per-process counter alone
	// gives every replica the same names, so ungrouped subscribers in two
	// replicas would share (and on shutdown destroy) one group, and group
	// members in two replicas would act as one consumer.
	name := b.instance + "-" + strconv.FormatUint(id, 10)
	group := cfg.Group
	ephemeral := false
	if group == "" {
		group = "bosun-" + name
		ephemeral = true
	}
	s := &subscription{
		id: id, subject: subject, group: group, ephemeral: ephemeral,
		consumer: name, quit: make(chan struct{}), bus: b,
	}
	b.subs[id] = s
	b.mu.Unlock()

	if err := s.createGroup(); err != nil {
		b.mu.Lock()
		delete(b.subs, id)
		b.mu.Unlock()
		return nil, err
	}
	b.wg.Add(1)
	go s.run(h)
	return &subHandle{bus: b, sub: s}, nil
}

// createGroup creates the subscription's group if it doesn't exist. "$" starts
// the group at new messages only, matching in-memory fan-out.
func (s *subscription) createGroup() error {
	if err := s.bus.rdb.XGroupCreateMkStream(s.bus.ctx, s.subject, s.group, "$").Err(); err != nil && !isBusyGroup(err) {
		return err
	}
	return nil
}

func (s *subscription) run(h eventmod.Handler) {
	defer s.bus.wg.Done()
	claimIdle := s.bus.Opts.claimIdle()
	var lastClaim time.Time
	for {
		select {
		case <-s.quit:
			return
		default:
		}
		if time.Since(lastClaim) >= claimIdle {
			s.claimStale(h, claimIdle)
			lastClaim = time.Now()
		}
		if ids := s.takeRetries(); len(ids) > 0 {
			s.redeliver(h, ids)
		}
		block := min(time.Second, claimIdle)
		if s.hasRetries() {
			block = -1 // don't wait for new messages while retries are queued
		}
		res, err := s.bus.rdb.XReadGroup(s.bus.ctx, &redis.XReadGroupArgs{
			Group: s.group, Consumer: s.consumer, Streams: []string{s.subject, ">"},
			Count: 16, Block: block,
		}).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				continue // block timeout, no messages
			}
			if s.bus.ctx.Err() != nil {
				return // bus closing
			}
			if strings.Contains(err.Error(), "NOGROUP") {
				_ = s.createGroup() // stream or group deleted out from under us
			}
			select {
			case <-s.quit:
				return
			case <-time.After(200 * time.Millisecond):
			}
			continue
		}
		for _, stream := range res {
			s.dispatch(h, stream.Messages, nil)
		}
	}
}

// claimStale takes over entries in the group that have been pending with any
// consumer (including a previous, crashed incarnation of this process) for at
// least idle, and hands them to h.
func (s *subscription) claimStale(h eventmod.Handler, idle time.Duration) {
	start := "0-0"
	for {
		msgs, next, err := s.bus.rdb.XAutoClaim(s.bus.ctx, &redis.XAutoClaimArgs{
			Stream: s.subject, Group: s.group, Consumer: s.consumer,
			MinIdle: idle, Start: start, Count: 16,
		}).Result()
		if err != nil {
			return // retried on the next sweep
		}
		if len(msgs) > 0 {
			s.dispatch(h, msgs, s.deliveryCounts(msgs))
		}
		if next == "0-0" || next == "" {
			return
		}
		start = next
		select {
		case <-s.quit:
			return
		default:
		}
	}
}

// redeliver claims nacked entries back to this consumer, which bumps their
// delivery count, and hands them to h again. On failure the IDs are queued
// again; the entries stay pending in the group either way, so none is lost.
func (s *subscription) redeliver(h eventmod.Handler, ids []string) {
	msgs, err := s.bus.rdb.XClaim(s.bus.ctx, &redis.XClaimArgs{
		Stream: s.subject, Group: s.group, Consumer: s.consumer, Messages: ids,
	}).Result()
	if err != nil {
		if s.bus.ctx.Err() != nil {
			return
		}
		for _, id := range ids {
			s.requeue(id)
		}
		select {
		case <-s.quit:
		case <-time.After(200 * time.Millisecond):
		}
		return
	}
	s.dispatch(h, msgs, s.deliveryCounts(msgs))
}

// deliveryCounts returns each claimed entry's delivery count from the group's
// pending entries list, keyed by entry ID.
func (s *subscription) deliveryCounts(msgs []redis.XMessage) map[string]int {
	cmds := make([]*redis.XPendingExtCmd, len(msgs))
	_, _ = s.bus.rdb.Pipelined(s.bus.ctx, func(p redis.Pipeliner) error {
		for i, m := range msgs {
			cmds[i] = p.XPendingExt(s.bus.ctx, &redis.XPendingExtArgs{
				Stream: s.subject, Group: s.group, Start: m.ID, End: m.ID, Count: 1,
			})
		}
		return nil
	})
	counts := make(map[string]int, len(msgs))
	for i, c := range cmds {
		if p, err := c.Result(); err == nil && len(p) == 1 {
			counts[msgs[i].ID] = int(p[0].RetryCount)
		}
	}
	return counts
}

// dispatch hands each entry to h. attempts, if set, overrides Attempt with
// the entry's delivery count.
func (s *subscription) dispatch(h eventmod.Handler, msgs []redis.XMessage, attempts map[string]int) {
	for _, m := range msgs {
		msg := decode(s.subject, m)
		if n := attempts[m.ID]; n > 0 {
			msg.Attempt = n
		}
		_ = h(context.Background(), &delivery{sub: s, id: m.ID, msg: msg})
	}
}

type delivery struct {
	sub  *subscription
	id   string
	msg  eventmod.Message
	once sync.Once
}

func (d *delivery) Message() eventmod.Message { return d.msg }

func (d *delivery) Ack(ctx context.Context) error {
	var err error
	d.once.Do(func() { err = d.sub.bus.rdb.XAck(d.sub.bus.ctx, d.sub.subject, d.sub.group, d.id).Err() })
	return err
}

// Nack with requeue leaves the entry pending (it is never acked here) and
// queues it for the subscription to claim back; without requeue it is acked
// and dropped.
func (d *delivery) Nack(ctx context.Context, requeue bool) error {
	var err error
	d.once.Do(func() {
		if requeue {
			d.sub.requeue(d.id)
			return
		}
		err = d.sub.bus.rdb.XAck(d.sub.bus.ctx, d.sub.subject, d.sub.group, d.id).Err()
	})
	return err
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
