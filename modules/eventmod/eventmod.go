// Package eventmod is the driver-agnostic contract for publish/subscribe
// messaging on top of Bosun.
//
// Services depend on eventmod.Publisher, eventmod.Subscriber, or eventmod.Bus;
// a driver module registers the concrete implementation. The in-memory driver
// (github.com/amberstack/bosun/modules/eventmemmod) is the default when no
// broker driver is imported; broker drivers (eventamqpmod, eventnatsmod,
// eventredismod) bind the same interfaces via bosun.DefaultBind, so a host
// swaps backends by importing a different driver — no application code changes.
//
//	import "github.com/amberstack/bosun/modules/eventmod"
//	import _ "github.com/amberstack/bosun/modules/eventmemmod" // default bus
//
//	type Publisher struct {
//	    Bus eventmod.Publisher // injected
//	}
//	func (p *Publisher) Emit(ctx context.Context) error {
//	    return eventmod.Topic[UserCreated]{Subject: "users.created"}.
//	        Publish(ctx, p.Bus, UserCreated{ID: 1})
//	}
//
// Delivery is at-least-once across every driver: a message may be redelivered
// after a Nack or a consumer crash, so handlers MUST be idempotent. Message.ID
// and Message.Attempt let consumers dedupe.
package eventmod

import (
	"context"
	"errors"
)

// Message is the transport-level unit a driver publishes and delivers. Data is
// the raw payload (the typed layer in typed.go JSON-encodes into it). Headers
// carry cross-cutting metadata — correlation id, W3C traceparent, tenant — that
// ride alongside the payload (see RegisterCarrier).
type Message struct {
	// Subject is the topic/routing key the message is published to.
	Subject string
	// Data is the raw payload bytes.
	Data []byte
	// Headers carry string metadata propagated across the boundary.
	Headers map[string]string
	// ID is a driver-assigned unique id, set on delivery. Use it to dedupe.
	ID string
	// Attempt is the 1-based delivery counter; >1 means a redelivery.
	Attempt int
}

// Header returns the header value for key, or "" if absent.
func (m Message) Header(key string) string {
	if m.Headers == nil {
		return ""
	}
	return m.Headers[key]
}

// Delivery is a received message plus its acknowledgement handle. Handlers run
// under manual-ack semantics only when the subscription opts in with
// WithManualAck; otherwise the typed layer Acks on success and Nacks on error
// automatically.
type Delivery interface {
	// Message returns the received message.
	Message() Message
	// Ack marks the message processed; the broker will not redeliver it.
	Ack(ctx context.Context) error
	// Nack marks processing failed. If requeue is true the broker should
	// redeliver (incrementing Attempt); if false the message is dropped or
	// dead-lettered per the driver's configuration.
	Nack(ctx context.Context, requeue bool) error
}

// Handler processes a single delivery. Returning an error signals failure;
// under auto-ack the delivery is Nacked with requeue.
type Handler func(ctx context.Context, d Delivery) error

// Publisher publishes raw payloads to a subject.
type Publisher interface {
	Publish(ctx context.Context, subject string, data []byte, opts ...PubOption) error
}

// Subscription is an active subscription that can be torn down.
type Subscription interface {
	Unsubscribe() error
}

// Subscriber registers a handler for messages on a subject.
type Subscriber interface {
	Subscribe(ctx context.Context, subject string, h Handler, opts ...SubOption) (Subscription, error)
}

// Bus is a combined Publisher + Subscriber. Drivers implement Bus and bind all
// three interfaces so services can depend on whichever they need.
type Bus interface {
	Publisher
	Subscriber
}

// --- publish options ---

// PubConfig is the resolved set of publish options.
type PubConfig struct {
	// Headers merged onto the outgoing message (in addition to carriers).
	Headers map[string]string
}

// PubOption configures a single Publish call.
type PubOption func(*PubConfig)

// WithHeaders attaches extra headers to the published message.
func WithHeaders(h map[string]string) PubOption {
	return func(c *PubConfig) {
		if c.Headers == nil {
			c.Headers = map[string]string{}
		}
		for k, v := range h {
			c.Headers[k] = v
		}
	}
}

// ResolvePub applies opts and returns the resolved config. Drivers call this.
func ResolvePub(opts []PubOption) PubConfig {
	var c PubConfig
	for _, o := range opts {
		o(&c)
	}
	return c
}

// --- subscribe options ---

// SubConfig is the resolved set of subscription options.
type SubConfig struct {
	// Group is a consumer-group / queue-group name. Members of the same group
	// share the subject's messages (competing consumers). Empty means every
	// subscriber receives every message (fan-out), where the driver supports it.
	Group string
	// Manual disables auto-ack; the handler must Ack/Nack the delivery itself.
	Manual bool
	// MaxInFlight bounds unacked deliveries dispatched concurrently (0 = driver default).
	MaxInFlight int
}

// SubOption configures a subscription.
type SubOption func(*SubConfig)

// WithGroup joins the subscription to a named consumer group (competing consumers).
func WithGroup(name string) SubOption { return func(c *SubConfig) { c.Group = name } }

// WithManualAck disables auto-ack; the handler must Ack/Nack.
func WithManualAck() SubOption { return func(c *SubConfig) { c.Manual = true } }

// WithMaxInFlight bounds concurrent unacked deliveries.
func WithMaxInFlight(n int) SubOption { return func(c *SubConfig) { c.MaxInFlight = n } }

// ResolveSub applies opts and returns the resolved config. Drivers call this.
func ResolveSub(opts []SubOption) SubConfig {
	var c SubConfig
	for _, o := range opts {
		o(&c)
	}
	return c
}

// --- portable errors ---

var (
	// ErrClosed is returned by Publish/Subscribe after the bus has shut down.
	ErrClosed = errors.New("eventmod: bus closed")
	// ErrNoSubscribers is returned by drivers that treat a publish with no
	// matching subscriber as an error (most do not; the in-memory bus does not).
	ErrNoSubscribers = errors.New("eventmod: no subscribers for subject")
)
