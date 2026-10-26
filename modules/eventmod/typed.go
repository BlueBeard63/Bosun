package eventmod

import (
	"context"
	"encoding/json"
	"fmt"
)

// Topic is a typed publish/subscribe handle for payload type T over a subject.
// It JSON-encodes on publish and decodes on consume, and runs all registered
// carriers so correlation/tenant metadata propagates automatically.
//
//	var UserCreated = eventmod.Topic[User]{Subject: "users.created"}
//	UserCreated.Publish(ctx, bus, User{ID: 1})
//	UserCreated.On(ctx, bus, func(ctx context.Context, u User) error { ... })
type Topic[T any] struct {
	Subject string
}

// Publish JSON-encodes v and publishes it to the topic's subject via p.
func (t Topic[T]) Publish(ctx context.Context, p Publisher, v T, opts ...PubOption) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("eventmod: marshal %s: %w", t.Subject, err)
	}
	// Merge carrier headers with any caller-supplied headers.
	m := Message{Subject: t.Subject}
	InjectContext(ctx, &m)
	if len(m.Headers) > 0 {
		opts = append([]PubOption{WithHeaders(m.Headers)}, opts...)
	}
	return p.Publish(ctx, t.Subject, data, opts...)
}

// On subscribes fn to the topic's subject via s. Each delivery is decoded into
// T; on success the delivery is Acked. On error it is redelivered after a
// backoff (WithBackoff) until it has been delivered WithMaxAttempts times,
// then published to the dead-letter subject (WithDeadLetter) and acked, or
// acked and logged if no dead-letter subject is set. A payload that cannot be
// decoded is dead-lettered at once (or dropped without one). None of this
// applies when the subscription requested manual acks. The per-message
// context carries any propagated carrier values.
func (t Topic[T]) On(ctx context.Context, s Subscriber, fn func(context.Context, T) error, opts ...SubOption) (Subscription, error) {
	cfg := ResolveSub(opts)
	r, err := newRetrier(s, cfg)
	if err != nil {
		return nil, err
	}
	return s.Subscribe(ctx, t.Subject, func(dctx context.Context, d Delivery) error {
		msg := d.Message()
		dctx = ExtractContext(dctx, msg)
		var v T
		if err := json.Unmarshal(msg.Data, &v); err != nil {
			err = fmt.Errorf("eventmod: unmarshal %s: %w", t.Subject, err)
			// A malformed payload will never decode; dead-letter or drop it
			// rather than redeliver it. Manual subscribers decide for
			// themselves.
			if !cfg.Manual {
				if r.pub != nil {
					r.giveUp(dctx, d, msg, max(msg.Attempt, 1), err)
				} else {
					_ = d.Nack(dctx, false)
				}
			}
			return err
		}
		herr := fn(dctx, v)
		if cfg.Manual {
			return herr
		}
		if herr != nil {
			r.fail(dctx, d, herr)
			return herr
		}
		return d.Ack(dctx)
	}, opts...)
}

// On is a package-level convenience mirroring Topic.On for callers that prefer
// an inline subject.
func On[T any](ctx context.Context, s Subscriber, subject string, fn func(context.Context, T) error, opts ...SubOption) (Subscription, error) {
	return Topic[T]{Subject: subject}.On(ctx, s, fn, opts...)
}
