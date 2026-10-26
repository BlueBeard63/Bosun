package eventredismod_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bluebeard63/bosun/modules/eventmod"
)

// A handler that always fails is retried WithMaxAttempts times with backoff,
// then dead-lettered with the bookkeeping headers, and not delivered again.
func TestRetryThenDeadLetter(t *testing.T) {
	b := newBus(t)
	subj := uniqueSubject()
	dlq := uniqueSubject() + ".dlq"
	dead := make(chan eventmod.Message, 2)
	if _, err := b.Subscribe(context.Background(), dlq, func(ctx context.Context, d eventmod.Delivery) error {
		dead <- d.Message()
		return d.Ack(ctx)
	}); err != nil {
		t.Fatalf("subscribe dlq: %v", err)
	}
	var calls atomic.Int64
	if _, err := eventmod.On[string](context.Background(), b, subj, func(context.Context, string) error {
		calls.Add(1)
		return errors.New("always fails")
	},
		eventmod.WithMaxAttempts(3),
		eventmod.WithBackoff(eventmod.ExponentialBackoff(10*time.Millisecond, 100*time.Millisecond)),
		eventmod.WithDeadLetter(dlq),
	); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	time.Sleep(50 * time.Millisecond) // let the readers start
	if err := (eventmod.Topic[string]{Subject: subj}).Publish(context.Background(), b, "poison"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	select {
	case m := <-dead:
		if string(m.Data) != `"poison"` || m.Attempt != 1 {
			t.Fatalf("dead-letter message = %q attempt %d", m.Data, m.Attempt)
		}
		if m.Header(eventmod.HeaderDeadLetterSubject) != subj ||
			m.Header(eventmod.HeaderDeadLetterAttempts) != "3" ||
			m.Header(eventmod.HeaderDeadLetterError) != "always fails" {
			t.Fatalf("dead-letter headers = %v", m.Headers)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("message never dead-lettered (handler ran %d times)", calls.Load())
	}
	time.Sleep(200 * time.Millisecond)
	if n := calls.Load(); n != 3 {
		t.Fatalf("handler ran %d times, want 3", n)
	}
}
