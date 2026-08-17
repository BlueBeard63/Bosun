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
// T; on success the delivery is Acked, on error it is Nacked with requeue
// (unless the subscription requested manual acks). The per-message context
// carries any propagated carrier values.
func (t Topic[T]) On(ctx context.Context, s Subscriber, fn func(context.Context, T) error, opts ...SubOption) (Subscription, error) {
	cfg := ResolveSub(opts)
	return s.Subscribe(ctx, t.Subject, func(dctx context.Context, d Delivery) error {
		msg := d.Message()
		dctx = ExtractContext(dctx, msg)
		var v T
		if err := json.Unmarshal(msg.Data, &v); err != nil {
			// A malformed payload will never decode; drop it rather than
			// redeliver forever. Manual subscribers decide for themselves.
			if !cfg.Manual {
				_ = d.Nack(dctx, false)
			}
			return fmt.Errorf("eventmod: unmarshal %s: %w", t.Subject, err)
		}
		herr := fn(dctx, v)
		if cfg.Manual {
			return herr
		}
		if herr != nil {
			_ = d.Nack(dctx, true)
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
