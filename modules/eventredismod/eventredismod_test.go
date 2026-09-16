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
