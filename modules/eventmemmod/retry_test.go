package eventmemmod_test

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bluebeard63/bosun/modules/eventmemmod"
	"github.com/bluebeard63/bosun/modules/eventmod"
)

// A handler that always fails is retried WithMaxAttempts times with growing
// delays, then the message lands on the dead-letter subject with its original
// headers plus the dead-letter bookkeeping, and is not delivered again.
func TestRetryBackoffThenDeadLetter(t *testing.T) {
	bus := eventmemmod.New()
	t.Cleanup(func() { _ = bus.Close() })
	ctx := context.Background()

	dead := make(chan eventmod.Message, 4)
	if _, err := bus.Subscribe(ctx, "orders.dlq", func(ctx context.Context, d eventmod.Delivery) error {
		dead <- d.Message()
		return d.Ack(ctx)
	}); err != nil {
		t.Fatalf("subscribe dlq: %v", err)
	}

	var mu sync.Mutex
	var calls []time.Time
	var delays []int // attempts the backoff was asked about
	backoff := eventmod.ExponentialBackoff(20*time.Millisecond, time.Second)
	if _, err := eventmod.On[string](ctx, bus, "orders.placed", func(context.Context, string) error {
		mu.Lock()
		calls = append(calls, time.Now())
		mu.Unlock()
		return errors.New("downstream said 400")
	},
		eventmod.WithMaxAttempts(3),
		eventmod.WithBackoff(func(attempt int) time.Duration {
			mu.Lock()
			delays = append(delays, attempt)
			mu.Unlock()
			return backoff(attempt)
		}),
		eventmod.WithDeadLetter("orders.dlq"),
	); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if err := (eventmod.Topic[string]{Subject: "orders.placed"}).Publish(ctx, bus, "o-1",
		eventmod.WithHeaders(map[string]string{"X-Tenant": "acme"})); err != nil {
		t.Fatalf("publish: %v", err)
	}

	var m eventmod.Message
	select {
	case m = <-dead:
	case <-time.After(5 * time.Second):
		t.Fatal("message was never dead-lettered")
	}
	if string(m.Data) != `"o-1"` {
		t.Fatalf("dead-letter data = %s", m.Data)
	}
	if m.Attempt != 1 {
		t.Fatalf("dead-letter Attempt = %d, want 1 (a fresh message)", m.Attempt)
	}
	for k, want := range map[string]string{
		eventmod.HeaderDeadLetterSubject:  "orders.placed",
		eventmod.HeaderDeadLetterAttempts: "3",
		eventmod.HeaderDeadLetterError:    "downstream said 400",
		"X-Tenant":                        "acme",
	} {
		if got := m.Header(k); got != want {
			t.Fatalf("header %s = %q, want %q", k, got, want)
		}
	}
	if m.Header(eventmod.HeaderDeadLetterID) == "" {
		t.Fatalf("dead-letter message lost the original ID")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 3 {
		t.Fatalf("handler ran %d times, want 3", len(calls))
	}
	if len(delays) != 2 || delays[0] != 1 || delays[1] != 2 {
		t.Fatalf("backoff consulted for attempts %v, want [1 2]", delays)
	}
	// Timers never fire early, so the gaps are at least the backoff.
	if g := calls[1].Sub(calls[0]); g < 20*time.Millisecond {
		t.Fatalf("first retry after %v, want >= 20ms", g)
	}
	if g := calls[2].Sub(calls[1]); g < 40*time.Millisecond {
		t.Fatalf("second retry after %v, want >= 40ms", g)
	}
}

// Without a dead-letter subject the message is acked (and logged) after the
// last attempt rather than looping.
func TestRetryDropsAfterMaxAttemptsWithoutDeadLetter(t *testing.T) {
	bus := eventmemmod.New()
	t.Cleanup(func() { _ = bus.Close() })
	ctx := context.Background()

	var calls atomic.Int64
	if _, err := eventmod.On[string](ctx, bus, "s", func(context.Context, string) error {
		calls.Add(1)
		return errors.New("boom")
	}, eventmod.WithMaxAttempts(2), eventmod.WithBackoff(nil)); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := (eventmod.Topic[string]{Subject: "s"}).Publish(ctx, bus, "x"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	waitFor(t, func() bool { return calls.Load() >= 2 })
	time.Sleep(50 * time.Millisecond)
	if n := calls.Load(); n != 2 {
		t.Fatalf("handler ran %d times, want 2", n)
	}
}

// The in-memory bus delays a redelivery with a timer, so a message waiting
// out its backoff doesn't hold up the messages behind it.
func TestBackoffDoesNotBlockOtherMessages(t *testing.T) {
	bus := eventmemmod.New()
	t.Cleanup(func() { _ = bus.Close() })
	ctx := context.Background()

	ok := make(chan string, 1)
	if _, err := eventmod.On[string](ctx, bus, "s", func(_ context.Context, v string) error {
		if v == "poison" {
			return errors.New("boom")
		}
		ok <- v
		return nil
	}, eventmod.WithBackoff(eventmod.ConstantBackoff(time.Hour))); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	topic := eventmod.Topic[string]{Subject: "s"}
	if err := topic.Publish(ctx, bus, "poison"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := topic.Publish(ctx, bus, "good"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	select {
	case v := <-ok:
		if v != "good" {
			t.Fatalf("got %q", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a backing-off message blocked the next one")
	}
}

// A payload that can't be decoded is dead-lettered straight away.
func TestUndecodableMessageDeadLettered(t *testing.T) {
	bus := eventmemmod.New()
	t.Cleanup(func() { _ = bus.Close() })
	ctx := context.Background()

	dead := make(chan eventmod.Message, 1)
	if _, err := bus.Subscribe(ctx, "dlq", func(ctx context.Context, d eventmod.Delivery) error {
		dead <- d.Message()
		return d.Ack(ctx)
	}); err != nil {
		t.Fatalf("subscribe dlq: %v", err)
	}
	var calls atomic.Int64
	if _, err := eventmod.On[int](ctx, bus, "n", func(context.Context, int) error {
		calls.Add(1)
		return nil
	}, eventmod.WithDeadLetter("dlq")); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := bus.Publish(ctx, "n", []byte("not json")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	select {
	case m := <-dead:
		if m.Header(eventmod.HeaderDeadLetterAttempts) != "1" ||
			!strings.Contains(m.Header(eventmod.HeaderDeadLetterError), "unmarshal") {
			t.Fatalf("dead-letter headers = %v", m.Headers)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("undecodable message was not dead-lettered")
	}
	if calls.Load() != 0 {
		t.Fatal("handler ran for an undecodable message")
	}
}

type subscriberOnly struct{ eventmod.Subscriber }

func TestDeadLetterNeedsPublisher(t *testing.T) {
	bus := eventmemmod.New()
	t.Cleanup(func() { _ = bus.Close() })
	_, err := eventmod.On[string](context.Background(), subscriberOnly{bus}, "s",
		func(context.Context, string) error { return nil }, eventmod.WithDeadLetter("dlq"))
	if err == nil {
		t.Fatal("WithDeadLetter on a subscriber that can't publish should fail")
	}
}

// A poison message retried forever with no backoff, behind a full buffer,
// must not grow the goroutine count: overflow and retries share one queue
// drained by a single goroutine per subscription.
func TestPoisonMessageGoroutinesBounded(t *testing.T) {
	bus := eventmemmod.New()
	t.Cleanup(func() { _ = bus.Close() })
	ctx := context.Background()
	baseline := runtime.NumGoroutine()

	gate := make(chan struct{})
	var release sync.Once
	open := func() { release.Do(func() { close(gate) }) }
	t.Cleanup(open) // runs before bus.Close, so a failed check can't hang it
	var calls atomic.Int64
	if _, err := eventmod.On[string](ctx, bus, "poison", func(context.Context, string) error {
		<-gate
		calls.Add(1)
		return errors.New("always fails")
	}, eventmod.WithMaxAttempts(0), eventmod.WithBackoff(nil)); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// The handler is stuck on the first message, so all but 1024 of these
	// overflow the subscription's buffer.
	const n = 3000
	topic := eventmod.Topic[string]{Subject: "poison"}
	for i := 0; i < n; i++ {
		if err := topic.Publish(ctx, bus, "bad"); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	const slack = 20
	if g := runtime.NumGoroutine(); g > baseline+slack {
		t.Fatalf("%d goroutines with a full buffer (baseline %d)", g, baseline)
	}

	// Now every message fails and is requeued as fast as it can be.
	open()
	waitFor(t, func() bool { return calls.Load() >= n+500 }) // some are retries
	if g := runtime.NumGoroutine(); g > baseline+slack {
		t.Fatalf("%d goroutines under a poison retry loop (baseline %d)", g, baseline)
	}
}
