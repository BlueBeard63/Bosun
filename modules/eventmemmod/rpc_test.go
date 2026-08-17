package eventmemmod_test

import (
	"context"
	"errors"
	"testing"

	"github.com/amberstack/bosun/modules/eventmemmod"
	"github.com/amberstack/bosun/modules/eventmod"
)

func TestRPCRoundTrip(t *testing.T) {
	bus := eventmemmod.New()
	t.Cleanup(func() { _ = bus.Close() })

	if _, err := eventmod.OnRequest[int, int](context.Background(), bus, "square", func(_ context.Context, n int) (int, error) {
		return n * n, nil
	}); err != nil {
		t.Fatalf("respond: %v", err)
	}
	got, err := eventmod.Call[int, int](context.Background(), bus, "square", 7)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if got != 49 {
		t.Fatalf("got %d, want 49", got)
	}
}

func TestRPCError(t *testing.T) {
	bus := eventmemmod.New()
	t.Cleanup(func() { _ = bus.Close() })

	if _, err := eventmod.OnRequest[int, int](context.Background(), bus, "boom", func(_ context.Context, n int) (int, error) {
		return 0, errors.New("kaboom")
	}); err != nil {
		t.Fatalf("respond: %v", err)
	}
	if _, err := eventmod.Call[int, int](context.Background(), bus, "boom", 1); err == nil {
		t.Fatal("expected error from responder")
	}
}

func TestRPCNoResponder(t *testing.T) {
	bus := eventmemmod.New()
	t.Cleanup(func() { _ = bus.Close() })

	_, err := eventmod.Call[int, int](context.Background(), bus, "nobody", 1)
	if !errors.Is(err, eventmod.ErrNoResponder) {
		t.Fatalf("expected ErrNoResponder, got %v", err)
	}
}

func TestRPCLoadBalances(t *testing.T) {
	bus := eventmemmod.New()
	t.Cleanup(func() { _ = bus.Close() })

	// Two responders on the same subject should be picked round-robin.
	tag := func(id int) func(context.Context, int) (int, error) {
		return func(_ context.Context, _ int) (int, error) { return id, nil }
	}
	_, _ = eventmod.OnRequest[int, int](context.Background(), bus, "who", tag(1))
	_, _ = eventmod.OnRequest[int, int](context.Background(), bus, "who", tag(2))

	seen := map[int]int{}
	for i := 0; i < 4; i++ {
		v, err := eventmod.Call[int, int](context.Background(), bus, "who", 0)
		if err != nil {
			t.Fatalf("call: %v", err)
		}
		seen[v]++
	}
	if seen[1] == 0 || seen[2] == 0 {
		t.Fatalf("expected both responders used, got %v", seen)
	}
}
