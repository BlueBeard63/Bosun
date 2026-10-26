package eventamqpmod_test

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bluebeard63/bosun/modules/eventamqpmod"
	"github.com/bluebeard63/bosun/modules/eventmod"
	amqp "github.com/rabbitmq/amqp091-go"
)

// Integration tests run only when BOSUN_AMQP_URL points at a RabbitMQ server,
// e.g. `docker run -p 5672:5672 rabbitmq` then
// BOSUN_AMQP_URL=amqp://guest:guest@localhost:5672/.
func newBus(t *testing.T) *eventamqpmod.Bus {
	t.Helper()
	return newBusWith(t, func(*eventamqpmod.Options) {})
}

func newBusWith(t *testing.T, configure func(*eventamqpmod.Options)) *eventamqpmod.Bus {
	t.Helper()
	url := os.Getenv("BOSUN_AMQP_URL")
	if url == "" {
		t.Skip("set BOSUN_AMQP_URL to run the RabbitMQ integration tests")
	}
	opts := &eventamqpmod.Options{URL: url, Exchange: "bosun.test", Prefetch: 16}
	configure(opts)
	b := &eventamqpmod.Bus{Opts: opts}
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

// A Nack in group A re-enqueues on A's queue only; group B sees the message once.
func TestNackDoesNotRedeliverToOtherGroups(t *testing.T) {
	b := newBus(t)
	subj := "bosuntest." + suffix()
	retried := make(chan eventmod.Message, 4)
	var bCount atomic.Int64
	if _, err := b.Subscribe(context.Background(), subj, func(_ context.Context, d eventmod.Delivery) error {
		if d.Message().Attempt == 1 {
			return d.Nack(context.Background(), true)
		}
		retried <- d.Message()
		return d.Ack(context.Background())
	}, eventmod.WithGroup("a-"+suffix())); err != nil {
		t.Fatalf("subscribe a: %v", err)
	}
	if _, err := b.Subscribe(context.Background(), subj, func(_ context.Context, d eventmod.Delivery) error {
		bCount.Add(1)
		return d.Ack(context.Background())
	}, eventmod.WithGroup("b-"+suffix())); err != nil {
		t.Fatalf("subscribe b: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if err := b.Publish(context.Background(), subj, []byte("x")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	select {
	case m := <-retried:
		if m.Attempt != 2 {
			t.Fatalf("redelivered with attempt %d, want 2", m.Attempt)
		}
		if m.Subject != subj {
			t.Fatalf("redelivered with subject %q, want %q", m.Subject, subj)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("group A never got the redelivery")
	}
	time.Sleep(300 * time.Millisecond)
	if got := bCount.Load(); got != 1 {
		t.Fatalf("group B received %d copies, want 1", got)
	}
}

// When the requeue publish fails, Nack returns the error and the original is
// handed back to the broker rather than acked, so it is redelivered.
func TestNackFailedRequeueRedelivers(t *testing.T) {
	b := newBus(t)
	subj := "bosuntest." + suffix()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel() // a dead ctx makes the requeue publish fail
	nackErr := make(chan error, 1)
	redelivered := make(chan struct{}, 1)
	var calls atomic.Int64
	if _, err := b.Subscribe(context.Background(), subj, func(_ context.Context, d eventmod.Delivery) error {
		if calls.Add(1) == 1 {
			nackErr <- d.Nack(cancelled, true)
			return nil
		}
		redelivered <- struct{}{}
		return d.Ack(context.Background())
	}, eventmod.WithGroup("g-"+suffix())); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if err := b.Publish(context.Background(), subj, []byte("x")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	select {
	case err := <-nackErr:
		if err == nil {
			t.Fatal("Nack returned nil although the requeue publish failed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no delivery")
	}
	select {
	case <-redelivered:
	case <-time.After(5 * time.Second):
		t.Fatal("message lost after a failed requeue")
	}
}

// Every delivery carries a non-empty ID that stays the same on redelivery.
func TestMessageIDStable(t *testing.T) {
	b := newBus(t)
	subj := "bosuntest." + suffix()
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
	time.Sleep(100 * time.Millisecond)
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

// One group on two subjects gets a queue per subject, so each handler sees
// only its own subject's messages.
func TestGroupSubjectsIsolated(t *testing.T) {
	b := newBus(t)
	invoices := "bosuntest.invoice." + suffix()
	refunds := "bosuntest.refund." + suffix()
	group := "billing-" + suffix()
	type seen struct{ subject, handler string }
	got := make(chan seen, 16)
	for _, subj := range []string{invoices, refunds} {
		subj := subj
		if _, err := b.Subscribe(context.Background(), subj, func(_ context.Context, d eventmod.Delivery) error {
			got <- seen{subject: d.Message().Subject, handler: subj}
			return d.Ack(context.Background())
		}, eventmod.WithGroup(group)); err != nil {
			t.Fatalf("subscribe %s: %v", subj, err)
		}
	}
	time.Sleep(150 * time.Millisecond)
	const n = 4
	for i := 0; i < n; i++ {
		for _, subj := range []string{invoices, refunds} {
			if err := b.Publish(context.Background(), subj, []byte("x")); err != nil {
				t.Fatalf("publish: %v", err)
			}
		}
	}
	for i := 0; i < 2*n; i++ {
		select {
		case s := <-got:
			if s.subject != s.handler {
				t.Fatalf("handler for %s received a message for %s", s.handler, s.subject)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("received %d of %d messages", i, 2*n)
		}
	}
	select {
	case s := <-got:
		t.Fatalf("unexpected extra delivery %+v", s)
	case <-time.After(200 * time.Millisecond):
	}
}

// Message.Subject is the concrete routing key, not the wildcard pattern.
func TestWildcardReportsRoutingKey(t *testing.T) {
	b := newBus(t)
	prefix := "bosuntest." + suffix()
	got := make(chan string, 1)
	if _, err := b.Subscribe(context.Background(), prefix+".>", func(_ context.Context, d eventmod.Delivery) error {
		got <- d.Message().Subject
		return d.Ack(context.Background())
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := b.Publish(context.Background(), prefix+".eu.created", []byte("x")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	select {
	case s := <-got:
		if s != prefix+".eu.created" {
			t.Fatalf("subject %q, want %q", s, prefix+".eu.created")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no delivery")
	}
}

func TestRPCSurvivesReconnect(t *testing.T) {
	b := newBus(t)
	subj := "bosuntest." + suffix()
	if _, err := eventmod.OnRequest[int, int](context.Background(), b, subj, func(_ context.Context, n int) (int, error) {
		return n * n, nil
	}); err != nil {
		t.Fatalf("respond: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	b.DropConnection()

	deadline := time.Now().Add(15 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		got, err := eventmod.Call[int, int](ctx, b, subj, 7)
		cancel()
		if err == nil {
			if got != 49 {
				t.Fatalf("got %d, want 49", got)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("RPC never recovered after reconnect: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// A queue that rejects publishes once full makes the broker nack the confirm.
func TestPublishReturnsErrorWhenBrokerNacks(t *testing.T) {
	b := newBus(t)
	subj := "bosuntest." + suffix()
	conn, err := amqp.Dial(os.Getenv("BOSUN_AMQP_URL"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("channel: %v", err)
	}
	q, err := ch.QueueDeclare("", false, true, false, false, amqp.Table{
		"x-max-length": int32(1),
		"x-overflow":   "reject-publish",
	})
	if err != nil {
		t.Fatalf("declare: %v", err)
	}
	if err := ch.QueueBind(q.Name, subj, "bosun.test", false, nil); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if err := b.Publish(context.Background(), subj, []byte("1")); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if err := b.Publish(context.Background(), subj, []byte("2")); !errors.Is(err, eventamqpmod.ErrNacked) {
		t.Fatalf("publish to a full reject-publish queue: got %v, want ErrNacked", err)
	}
}

func TestMandatoryPublishUnroutable(t *testing.T) {
	b := newBusWith(t, func(o *eventamqpmod.Options) { o.Mandatory = true })
	err := b.Publish(context.Background(), "bosuntest.nobody."+suffix(), []byte("x"))
	if !errors.Is(err, eventamqpmod.ErrUnroutable) {
		t.Fatalf("got %v, want ErrUnroutable", err)
	}
}
