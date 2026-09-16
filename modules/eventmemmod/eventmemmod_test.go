package eventmemmod_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bluebeard63/bosun/modules/eventmod"
	"github.com/bluebeard63/bosun/modules/eventmemmod"
)

// waitFor polls cond up to a deadline so tests don't sleep for fixed periods.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met before deadline")
}

func TestPublishSubscribeRoundTrip(t *testing.T) {
	bus := eventmemmod.New()
	t.Cleanup(func() { _ = bus.Close() })

	var got atomic.Value
	_, err := eventmod.On[string](context.Background(), bus, "greetings", func(_ context.Context, s string) error {
		got.Store(s)
		return nil
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if err := (eventmod.Topic[string]{Subject: "greetings"}).Publish(context.Background(), bus, "hello"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	waitFor(t, func() bool { return got.Load() == "hello" })
}

func TestFanOutToDistinctSubscribers(t *testing.T) {
	bus := eventmemmod.New()
	t.Cleanup(func() { _ = bus.Close() })

	var a, b atomic.Int64
	sub := func(counter *atomic.Int64) {
		_, err := bus.Subscribe(context.Background(), "evt", func(_ context.Context, d eventmod.Delivery) error {
			counter.Add(1)
			return d.Ack(context.Background())
		})
		if err != nil {
			t.Fatalf("subscribe: %v", err)
		}
	}
	sub(&a)
	sub(&b)

	if err := bus.Publish(context.Background(), "evt", []byte("x")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	waitFor(t, func() bool { return a.Load() == 1 && b.Load() == 1 })
}

func TestConsumerGroupCompetes(t *testing.T) {
	bus := eventmemmod.New()
	t.Cleanup(func() { _ = bus.Close() })

	var total atomic.Int64
	for i := 0; i < 3; i++ {
		_, err := bus.Subscribe(context.Background(), "work", func(_ context.Context, d eventmod.Delivery) error {
			total.Add(1)
			return d.Ack(context.Background())
		}, eventmod.WithGroup("workers"))
		if err != nil {
			t.Fatalf("subscribe: %v", err)
		}
	}

	const n = 9
	for i := 0; i < n; i++ {
		if err := bus.Publish(context.Background(), "work", []byte("j")); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	// Each message goes to exactly one group member -> total == n (not n*members).
	waitFor(t, func() bool { return total.Load() == n })
	time.Sleep(20 * time.Millisecond)
	if got := total.Load(); got != n {
		t.Fatalf("group over-delivered: got %d want %d", got, n)
	}
}

func TestNackRequeueRedelivers(t *testing.T) {
	bus := eventmemmod.New()
	t.Cleanup(func() { _ = bus.Close() })

	var attempts atomic.Int64
	done := make(chan int, 1)
	_, err := bus.Subscribe(context.Background(), "flaky", func(_ context.Context, d eventmod.Delivery) error {
		n := attempts.Add(1)
		if d.Message().Attempt < 3 {
			return d.Nack(context.Background(), true)
		}
		done <- int(n)
		return d.Ack(context.Background())
	}, eventmod.WithManualAck())
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if err := bus.Publish(context.Background(), "flaky", []byte("x")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	select {
	case n := <-done:
		if n != 3 {
			t.Fatalf("expected success on attempt 3, got %d attempts", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("message never succeeded after redelivery")
	}
}

func TestWildcardMatch(t *testing.T) {
	bus := eventmemmod.New()
	t.Cleanup(func() { _ = bus.Close() })

	var hits atomic.Int64
	_, err := bus.Subscribe(context.Background(), "orders.*.created", func(_ context.Context, d eventmod.Delivery) error {
		hits.Add(1)
		return d.Ack(context.Background())
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	_ = bus.Publish(context.Background(), "orders.eu.created", []byte("1"))
	_ = bus.Publish(context.Background(), "orders.us.created", []byte("2"))
	_ = bus.Publish(context.Background(), "orders.eu.shipped", []byte("3")) // no match
	waitFor(t, func() bool { return hits.Load() == 2 })
	time.Sleep(20 * time.Millisecond)
	if hits.Load() != 2 {
		t.Fatalf("wildcard matched wrong count: %d", hits.Load())
	}
}

func TestPublishAfterCloseErrors(t *testing.T) {
	bus := eventmemmod.New()
	if err := bus.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := bus.Publish(context.Background(), "x", nil); err != eventmod.ErrClosed {
		t.Fatalf("expected ErrClosed, got %v", err)
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	bus := eventmemmod.New()
	t.Cleanup(func() { _ = bus.Close() })

	var hits atomic.Int64
	sub, err := bus.Subscribe(context.Background(), "s", func(_ context.Context, d eventmod.Delivery) error {
		hits.Add(1)
		return d.Ack(context.Background())
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := sub.Unsubscribe(); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	_ = bus.Publish(context.Background(), "s", []byte("x"))
	time.Sleep(20 * time.Millisecond)
	if hits.Load() != 0 {
		t.Fatalf("delivered after unsubscribe: %d", hits.Load())
	}
}

// carrier propagation: a registered carrier should move a header value from the
// publish context onto the consume context.
type ctxKey struct{}

type testCarrier struct{}

func (testCarrier) Inject(ctx context.Context, h map[string]string) {
	if v, ok := ctx.Value(ctxKey{}).(string); ok {
		h["x-test"] = v
	}
}
func (testCarrier) Extract(ctx context.Context, h map[string]string) context.Context {
	if v, ok := h["x-test"]; ok {
		return context.WithValue(ctx, ctxKey{}, v)
	}
	return ctx
}

var registerOnce sync.Once

func TestCarrierPropagation(t *testing.T) {
	registerOnce.Do(func() { eventmod.RegisterCarrier(testCarrier{}) })
	bus := eventmemmod.New()
	t.Cleanup(func() { _ = bus.Close() })

	got := make(chan string, 1)
	_, err := eventmod.On[string](context.Background(), bus, "c", func(ctx context.Context, _ string) error {
		v, _ := ctx.Value(ctxKey{}).(string)
		got <- v
		return nil
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	pubCtx := context.WithValue(context.Background(), ctxKey{}, "trace-123")
	if err := (eventmod.Topic[string]{Subject: "c"}).Publish(pubCtx, bus, "hi"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	select {
	case v := <-got:
		if v != "trace-123" {
			t.Fatalf("carrier did not propagate: got %q", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no delivery")
	}
}
