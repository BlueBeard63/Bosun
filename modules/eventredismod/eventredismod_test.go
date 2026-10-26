package eventredismod_test

import (
	"context"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bluebeard63/bosun/modules/eventmod"
	"github.com/bluebeard63/bosun/modules/eventredismod"
	"github.com/redis/go-redis/v9"
)

// Integration tests run only when BOSUN_REDIS_ADDR points at a Redis server,
// e.g. `docker run -p 6379:6379 redis` then BOSUN_REDIS_ADDR=localhost:6379.
func newBus(t *testing.T) *eventredismod.Bus {
	t.Helper()
	addr := os.Getenv("BOSUN_REDIS_ADDR")
	if addr == "" {
		t.Skip("set BOSUN_REDIS_ADDR to run the Redis integration tests")
	}
	b := &eventredismod.Bus{Opts: &eventredismod.Options{Addr: addr}}
	if err := b.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func uniqueSubject() string {
	return "bosuntest." + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func TestRoundTrip(t *testing.T) {
	b := newBus(t)
	subj := uniqueSubject()
	got := make(chan string, 1)
	if _, err := eventmod.On[string](context.Background(), b, subj, func(_ context.Context, s string) error {
		got <- s
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	time.Sleep(50 * time.Millisecond) // let the reader start
	if err := (eventmod.Topic[string]{Subject: subj}).Publish(context.Background(), b, "hello"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	select {
	case v := <-got:
		if v != "hello" {
			t.Fatalf("got %q", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no delivery")
	}
}

func TestGroupCompetes(t *testing.T) {
	b := newBus(t)
	subj := uniqueSubject()
	var total atomic.Int64
	for i := 0; i < 3; i++ {
		if _, err := b.Subscribe(context.Background(), subj, func(_ context.Context, d eventmod.Delivery) error {
			total.Add(1)
			return d.Ack(context.Background())
		}, eventmod.WithGroup("workers")); err != nil {
			t.Fatalf("subscribe: %v", err)
		}
	}
	time.Sleep(50 * time.Millisecond)
	const n = 9
	for i := 0; i < n; i++ {
		if err := b.Publish(context.Background(), subj, []byte("j")); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && total.Load() < n {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if got := total.Load(); got != n {
		t.Fatalf("group delivered %d, want %d", got, n)
	}
}

// A Nack in group A keeps the entry pending in A and claims it back there;
// group B and an ungrouped subscriber each see the message once.
func TestNackDoesNotRedeliverToOtherGroups(t *testing.T) {
	b := newBus(t)
	subj := uniqueSubject()
	retried := make(chan eventmod.Message, 4)
	var bCount, plainCount atomic.Int64
	if _, err := b.Subscribe(context.Background(), subj, func(_ context.Context, d eventmod.Delivery) error {
		if d.Message().Attempt == 1 {
			return d.Nack(context.Background(), true)
		}
		retried <- d.Message()
		return d.Ack(context.Background())
	}, eventmod.WithGroup("a")); err != nil {
		t.Fatalf("subscribe a: %v", err)
	}
	if _, err := b.Subscribe(context.Background(), subj, func(_ context.Context, d eventmod.Delivery) error {
		bCount.Add(1)
		return d.Ack(context.Background())
	}, eventmod.WithGroup("b")); err != nil {
		t.Fatalf("subscribe b: %v", err)
	}
	if _, err := b.Subscribe(context.Background(), subj, func(_ context.Context, d eventmod.Delivery) error {
		plainCount.Add(1)
		return d.Ack(context.Background())
	}); err != nil {
		t.Fatalf("subscribe plain: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := b.Publish(context.Background(), subj, []byte("x")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	select {
	case m := <-retried:
		if m.Attempt != 2 {
			t.Fatalf("redelivered with attempt %d, want 2", m.Attempt)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("group A never got the redelivery")
	}
	time.Sleep(300 * time.Millisecond)
	if got := bCount.Load(); got != 1 {
		t.Fatalf("group B received %d copies, want 1", got)
	}
	if got := plainCount.Load(); got != 1 {
		t.Fatalf("ungrouped subscriber received %d copies, want 1", got)
	}
	rdb := redis.NewClient(&redis.Options{Addr: os.Getenv("BOSUN_REDIS_ADDR")})
	defer rdb.Close()
	if n, err := rdb.XLen(context.Background(), subj).Result(); err != nil || n != 1 {
		t.Fatalf("stream length %d (err %v), want 1: a Nack must not re-add the entry", n, err)
	}
}

// Every delivery carries a non-empty ID that stays the same on redelivery.
func TestMessageIDStable(t *testing.T) {
	b := newBus(t)
	subj := uniqueSubject()
	ids := make(chan string, 2)
	if _, err := b.Subscribe(context.Background(), subj, func(_ context.Context, d eventmod.Delivery) error {
		ids <- d.Message().ID
		if d.Message().Attempt == 1 {
			return d.Nack(context.Background(), true)
		}
		return d.Ack(context.Background())
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := b.Publish(context.Background(), subj, []byte("x")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	var got []string
	for len(got) < 2 {
		select {
		case id := <-ids:
			got = append(got, id)
		case <-time.After(5 * time.Second):
			t.Fatalf("got %d deliveries, want 2", len(got))
		}
	}
	if got[0] == "" || got[0] != got[1] {
		t.Fatalf("IDs %q then %q, want the same non-empty ID", got[0], got[1])
	}
}

func TestNackRedelivers(t *testing.T) {
	b := newBus(t)
	subj := uniqueSubject()
	done := make(chan int, 1)
	if _, err := b.Subscribe(context.Background(), subj, func(_ context.Context, d eventmod.Delivery) error {
		if d.Message().Attempt < 3 {
			return d.Nack(context.Background(), true)
		}
		done <- d.Message().Attempt
		return d.Ack(context.Background())
	}, eventmod.WithManualAck()); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := b.Publish(context.Background(), subj, []byte("x")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	select {
	case n := <-done:
		if n != 3 {
			t.Fatalf("succeeded on attempt %d, want 3", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("message never redelivered to attempt 3")
	}
}
