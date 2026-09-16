package eventamqpmod_test

import (
	"context"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bluebeard63/bosun/modules/eventamqpmod"
	"github.com/bluebeard63/bosun/modules/eventmod"
)

// Integration tests run only when BOSUN_AMQP_URL points at a RabbitMQ server,
// e.g. `docker run -p 5672:5672 rabbitmq` then
// BOSUN_AMQP_URL=amqp://guest:guest@localhost:5672/.
func newBus(t *testing.T) *eventamqpmod.Bus {
	t.Helper()
	url := os.Getenv("BOSUN_AMQP_URL")
	if url == "" {
		t.Skip("set BOSUN_AMQP_URL to run the RabbitMQ integration tests")
	}
	b := &eventamqpmod.Bus{Opts: &eventamqpmod.Options{URL: url, Exchange: "bosun.test", Prefetch: 16}}
	if err := b.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func suffix() string { return strconv.FormatInt(time.Now().UnixNano(), 36) }

func TestRoundTrip(t *testing.T) {
	b := newBus(t)
	subj := "bosuntest." + suffix()
	got := make(chan string, 1)
	if _, err := eventmod.On[string](context.Background(), b, subj, func(_ context.Context, s string) error {
		got <- s
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
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
	subj := "bosuntest." + suffix()
	group := "workers-" + suffix()
	var total atomic.Int64
	for i := 0; i < 3; i++ {
		if _, err := b.Subscribe(context.Background(), subj, func(_ context.Context, d eventmod.Delivery) error {
			total.Add(1)
			return d.Ack(context.Background())
		}, eventmod.WithGroup(group)); err != nil {
			t.Fatalf("subscribe: %v", err)
		}
	}
	time.Sleep(150 * time.Millisecond)
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
	subj := "bosuntest." + suffix()
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
	time.Sleep(100 * time.Millisecond)
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

func TestRPC(t *testing.T) {
	b := newBus(t)
	subj := "bosuntest." + suffix()
	if _, err := eventmod.OnRequest[int, int](context.Background(), b, subj, func(_ context.Context, n int) (int, error) {
		return n * n, nil
	}); err != nil {
		t.Fatalf("respond: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := eventmod.Call[int, int](ctx, b, subj, 6)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if got != 36 {
		t.Fatalf("got %d, want 36", got)
	}
}
