package eventmod

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"strconv"
	"time"
)

// Retry defaults applied by the typed layer (Topic.On) when a subscription
// sets no retry options of its own.
const (
	// DefaultMaxAttempts is how many times a failing message is delivered
	// before it is dead-lettered (or dropped with an error log).
	DefaultMaxAttempts = 10
	// DefaultBackoffBase and DefaultBackoffMax shape the default exponential
	// backoff: 100ms, 200ms, 400ms, … capped at 5s.
	DefaultBackoffBase = 100 * time.Millisecond
	DefaultBackoffMax  = 5 * time.Second
)

// Headers set on a dead-lettered message, alongside the original message's
// own headers (correlation id, traceparent, tenant, …).
const (
	// HeaderDeadLetterSubject is the subject the message was originally
	// published to.
	HeaderDeadLetterSubject = "Bosun-Dead-Letter-Subject"
	// HeaderDeadLetterAttempts is how many times the message was delivered.
	HeaderDeadLetterAttempts = "Bosun-Dead-Letter-Attempts"
	// HeaderDeadLetterError is the error the last attempt failed with.
	HeaderDeadLetterError = "Bosun-Dead-Letter-Error"
	// HeaderDeadLetterID is the original message's ID, if the driver set one.
	HeaderDeadLetterID = "Bosun-Dead-Letter-ID"
)

// driverHeaders are transport bookkeeping the broker drivers keep in message
// headers; they are stripped from a dead-letter copy so its consumer starts
// at attempt 1 on the dead-letter subject.
var driverHeaders = []string{"Bosun-Attempt", "Bosun-Subject"}

// Backoff returns how long to wait before redelivering a message whose
// attempt-th delivery (1-based) just failed.
type Backoff func(attempt int) time.Duration

// ExponentialBackoff doubles the delay on every attempt, starting at base and
// never exceeding limit: base, 2·base, 4·base, … limit.
func ExponentialBackoff(base, limit time.Duration) Backoff {
	return func(attempt int) time.Duration {
		if base <= 0 {
			return 0
		}
		d := base
		for i := 1; i < attempt && d < limit; i++ {
			d *= 2
		}
		return min(d, limit)
	}
}

// ConstantBackoff waits d before every redelivery. ConstantBackoff(0)
// redelivers immediately.
func ConstantBackoff(d time.Duration) Backoff {
	return func(int) time.Duration { return d }
}

// DelayedNacker is an optional Delivery extension for drivers that can
// schedule a redelivery themselves (for example with a timer) instead of
// having the typed layer wait in the handler goroutine before calling Nack.
// NackAfter behaves like Nack(ctx, true), except that the message is
// redelivered no sooner than delay from now.
type DelayedNacker interface {
	NackAfter(ctx context.Context, delay time.Duration) error
}

// WithMaxAttempts caps how many times the typed layer delivers a failing
// message: after the n-th failed attempt it is moved to the dead-letter
// subject (see WithDeadLetter), or acked and logged as an error if none is
// set. n <= 0 retries forever (the pre-v0.8 behaviour). The default is
// DefaultMaxAttempts.
func WithMaxAttempts(n int) SubOption {
	return func(c *SubConfig) {
		if n <= 0 {
			n = -1
		}
		c.MaxAttempts = n
	}
}

// WithBackoff sets the delay before each redelivery of a failing message.
// The default is ExponentialBackoff(DefaultBackoffBase, DefaultBackoffMax);
// WithBackoff(nil) redelivers immediately.
func WithBackoff(b Backoff) SubOption {
	return func(c *SubConfig) {
		if b == nil {
			b = ConstantBackoff(0)
		}
		c.Backoff = b
	}
}

// WithDeadLetter publishes messages that exhaust their attempts, and messages
// whose payload cannot be decoded, to subject before acking the original. The
// Subscriber passed to On must also be a Publisher (every bundled driver's
// Bus is).
func WithDeadLetter(subject string) SubOption {
	return func(c *SubConfig) { c.DeadLetter = subject }
}

// maxAttempts resolves the configured cap; 0 means unlimited.
func (c SubConfig) maxAttempts() int {
	switch {
	case c.MaxAttempts < 0:
		return 0
	case c.MaxAttempts == 0:
		return DefaultMaxAttempts
	}
	return c.MaxAttempts
}

func (c SubConfig) backoff() Backoff {
	if c.Backoff == nil {
		return ExponentialBackoff(DefaultBackoffBase, DefaultBackoffMax)
	}
	return c.Backoff
}

// retrier is the auto-ack failure policy for one subscription.
type retrier struct {
	cfg SubConfig
	pub Publisher // set when cfg.DeadLetter is
}

func newRetrier(s Subscriber, cfg SubConfig) (*retrier, error) {
	r := &retrier{cfg: cfg}
	if cfg.DeadLetter != "" && !cfg.Manual {
		p, ok := s.(Publisher)
		if !ok {
			return nil, fmt.Errorf("eventmod: WithDeadLetter(%q): subscriber %T is not a Publisher", cfg.DeadLetter, s)
		}
		r.pub = p
	}
	return r, nil
}

// fail settles a delivery whose handler returned herr: it schedules a
// redelivery after the backoff, or, once the attempt cap is reached,
// dead-letters the message and acks it.
func (r *retrier) fail(ctx context.Context, d Delivery, herr error) {
	msg := d.Message()
	attempt := max(msg.Attempt, 1)
	if limit := r.cfg.maxAttempts(); limit > 0 && attempt >= limit {
		r.giveUp(ctx, d, msg, attempt, herr)
		return
	}
	r.redeliver(ctx, d, attempt)
}

// redeliver nacks d for redelivery once the backoff for attempt has passed.
func (r *retrier) redeliver(ctx context.Context, d Delivery, attempt int) {
	delay := r.cfg.backoff()(attempt)
	if delay <= 0 {
		_ = d.Nack(ctx, true)
		return
	}
	if dn, ok := d.(DelayedNacker); ok {
		_ = dn.NackAfter(ctx, delay)
		return
	}
	// The driver can't delay a redelivery, so wait here. This holds the
	// handler goroutine (and so, on drivers that dispatch one message at a
	// time, the subscription) for at most the backoff's maximum delay.
	t := time.NewTimer(delay)
	select {
	case <-t.C:
	case <-ctx.Done():
		t.Stop()
	}
	_ = d.Nack(context.WithoutCancel(ctx), true)
}

// giveUp moves a message out of the retry loop: to the dead-letter subject if
// one is set, otherwise it is dropped with an error log. The original is
// acked only once the dead-letter copy is published; if that publish fails
// the message is redelivered (after the backoff) so it is not lost, and the
// dead-letter publish is tried again on that delivery.
func (r *retrier) giveUp(ctx context.Context, d Delivery, msg Message, attempt int, cause error) {
	if r.pub == nil {
		slog.Error("eventmod: dropping message after final attempt",
			"subject", msg.Subject, "id", msg.ID, "attempts", attempt, "error", cause)
		_ = d.Ack(ctx)
		return
	}
	if err := r.deadLetter(ctx, msg, attempt, cause); err != nil {
		slog.Error("eventmod: dead-letter publish failed; message will be redelivered",
			"subject", msg.Subject, "dead_letter", r.cfg.DeadLetter, "id", msg.ID, "error", err)
		r.redeliver(ctx, d, attempt)
		return
	}
	_ = d.Ack(ctx)
}

func (r *retrier) deadLetter(ctx context.Context, msg Message, attempt int, cause error) error {
	hdr := make(map[string]string, len(msg.Headers)+4)
	maps.Copy(hdr, msg.Headers)
	for _, k := range driverHeaders {
		delete(hdr, k)
	}
	hdr[HeaderDeadLetterSubject] = msg.Subject
	hdr[HeaderDeadLetterAttempts] = strconv.Itoa(attempt)
	if cause != nil {
		hdr[HeaderDeadLetterError] = cause.Error()
	}
	if msg.ID != "" {
		hdr[HeaderDeadLetterID] = msg.ID
	}
	return r.pub.Publish(ctx, r.cfg.DeadLetter, msg.Data, WithHeaders(hdr))
}
