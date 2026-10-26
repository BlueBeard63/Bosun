package eventmod

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestExponentialBackoff(t *testing.T) {
	b := ExponentialBackoff(100*time.Millisecond, time.Second)
	want := []time.Duration{100, 200, 400, 800, 1000, 1000}
	for i, w := range want {
		if got := b(i + 1); got != w*time.Millisecond {
			t.Fatalf("attempt %d: %v, want %v", i+1, got, w*time.Millisecond)
		}
	}
	if got := ExponentialBackoff(0, time.Second)(3); got != 0 {
		t.Fatalf("zero base: %v", got)
	}
}

func TestMaxAttemptsResolution(t *testing.T) {
	for _, tc := range []struct {
		opts []SubOption
		want int
	}{
		{nil, DefaultMaxAttempts},
		{[]SubOption{WithMaxAttempts(3)}, 3},
		{[]SubOption{WithMaxAttempts(0)}, 0},
		{[]SubOption{WithMaxAttempts(-5)}, 0},
	} {
		if got := ResolveSub(tc.opts).maxAttempts(); got != tc.want {
			t.Fatalf("%d options: maxAttempts %d, want %d", len(tc.opts), got, tc.want)
		}
	}
}

// fakeDelivery records how it was settled; it does not implement
// DelayedNacker, so the retrier has to wait before nacking.
type fakeDelivery struct {
	msg     Message
	acked   bool
	nacked  bool
	requeue bool
	at      time.Time
}

func (d *fakeDelivery) Message() Message { return d.msg }
func (d *fakeDelivery) Ack(context.Context) error {
	d.acked, d.at = true, time.Now()
	return nil
}
func (d *fakeDelivery) Nack(_ context.Context, requeue bool) error {
	d.nacked, d.requeue, d.at = true, requeue, time.Now()
	return nil
}

type fakePub struct {
	err     error
	subject string
	data    []byte
	headers map[string]string
}

func (p *fakePub) Publish(_ context.Context, subject string, data []byte, opts ...PubOption) error {
	if p.err != nil {
		return p.err
	}
	p.subject, p.data, p.headers = subject, data, ResolvePub(opts).Headers
	return nil
}

func TestRetrierWaitsBeforeNackWithoutDelayedNacker(t *testing.T) {
	r := &retrier{cfg: ResolveSub([]SubOption{WithBackoff(ConstantBackoff(30 * time.Millisecond))})}
	d := &fakeDelivery{msg: Message{Subject: "s", Attempt: 1}}
	start := time.Now()
	r.fail(context.Background(), d, errors.New("boom"))
	if !d.nacked || !d.requeue {
		t.Fatalf("want Nack(requeue), got acked=%v nacked=%v requeue=%v", d.acked, d.nacked, d.requeue)
	}
	if waited := d.at.Sub(start); waited < 30*time.Millisecond {
		t.Fatalf("nacked after %v, want >= 30ms", waited)
	}
}

func TestRetrierBackoffStopsOnContextCancel(t *testing.T) {
	r := &retrier{cfg: ResolveSub([]SubOption{WithBackoff(ConstantBackoff(time.Hour))})}
	d := &fakeDelivery{msg: Message{Subject: "s", Attempt: 1}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.fail(ctx, d, errors.New("boom"))
	if !d.nacked || !d.requeue {
		t.Fatal("a cancelled wait must still nack for redelivery")
	}
}

func TestRetrierDeadLettersOnLastAttempt(t *testing.T) {
	pub := &fakePub{}
	r := &retrier{cfg: ResolveSub([]SubOption{WithMaxAttempts(2), WithDeadLetter("dlq")}), pub: pub}
	d := &fakeDelivery{msg: Message{
		Subject: "orders", Data: []byte("x"), ID: "id-1", Attempt: 2,
		Headers: map[string]string{"Bosun-Attempt": "2", "Bosun-Subject": "orders", "X-Tenant": "acme"},
	}}
	r.fail(context.Background(), d, errors.New("boom"))
	if !d.acked {
		t.Fatal("original not acked after dead-lettering")
	}
	if pub.subject != "dlq" || string(pub.data) != "x" {
		t.Fatalf("published %q to %q", pub.data, pub.subject)
	}
	h := pub.headers
	if h[HeaderDeadLetterSubject] != "orders" || h[HeaderDeadLetterAttempts] != "2" ||
		h[HeaderDeadLetterError] != "boom" || h[HeaderDeadLetterID] != "id-1" || h["X-Tenant"] != "acme" {
		t.Fatalf("dead-letter headers = %v", h)
	}
	if _, ok := h["Bosun-Attempt"]; ok {
		t.Fatal("driver attempt header leaked onto the dead-letter copy")
	}
}

func TestRetrierRedeliversWhenDeadLetterPublishFails(t *testing.T) {
	pub := &fakePub{err: errors.New("broker down")}
	r := &retrier{cfg: ResolveSub([]SubOption{WithMaxAttempts(1), WithBackoff(nil), WithDeadLetter("dlq")}), pub: pub}
	d := &fakeDelivery{msg: Message{Subject: "s", Attempt: 1}}
	r.fail(context.Background(), d, errors.New("boom"))
	if d.acked || !d.nacked || !d.requeue {
		t.Fatal("a failed dead-letter publish must redeliver, not ack")
	}
}
