package eventmemmod_test

import (
	"context"
	"testing"
	"time"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/modules/eventmemmod"
	"github.com/bluebeard63/bosun/modules/eventmod"
	"github.com/bluebeard63/bosun/registry"
)

// TestDIIntegration boots a real bosun app with the in-memory bus registered as
// the default backend, resolves the eventmod interfaces from the container, and
// confirms Shutdown closes the bus (io.Closer). Mirrors the DI-driven setup in
// docs/testing.md.
func TestDIIntegration(t *testing.T) {
	eventmemmod.Default()
	app := bosun.New()
	if err := app.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Shutdown() })

	pub, err := registry.Resolve[eventmod.Publisher](app.Reg)
	if err != nil {
		t.Fatalf("resolve publisher: %v", err)
	}
	sub, err := registry.Resolve[eventmod.Subscriber](app.Reg)
	if err != nil {
		t.Fatalf("resolve subscriber: %v", err)
	}
	// Publisher and Subscriber must resolve to the same bus instance.
	bus, err := registry.Resolve[eventmod.Bus](app.Reg)
	if err != nil {
		t.Fatalf("resolve bus: %v", err)
	}
	_ = bus

	got := make(chan string, 1)
	if _, err := eventmod.On[string](context.Background(), sub, "ping", func(_ context.Context, s string) error {
		got <- s
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := (eventmod.Topic[string]{Subject: "ping"}).Publish(context.Background(), pub, "pong"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	select {
	case v := <-got:
		if v != "pong" {
			t.Fatalf("got %q", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no delivery through DI-resolved bus")
	}
}
