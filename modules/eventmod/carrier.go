package eventmod

import "context"

// Carrier copies cross-cutting values between a context and message headers so
// they survive the publish/consume boundary. It is the decoupling seam that
// lets correlation-id and tenant propagation (which live in other packages)
// ride on every message without eventmod importing those packages.
//
// A package registers a carrier at init via RegisterCarrier; the typed layer
// (Topic.Publish / On) invokes all carriers automatically. Inject writes the
// value from ctx into the outgoing headers; Extract reads it back off an
// incoming message and returns a ctx carrying it.
type Carrier interface {
	Inject(ctx context.Context, h map[string]string)
	Extract(ctx context.Context, h map[string]string) context.Context
}

var carriers []Carrier

// RegisterCarrier installs a Carrier applied to every typed publish and consume.
// Call it from a package init:
//
//	var _ = eventmod.RegisterCarrier(correlationCarrier{})
func RegisterCarrier(c Carrier) struct{} {
	carriers = append(carriers, c)
	return struct{}{}
}

// InjectContext copies every registered carrier's value from ctx into m.Headers.
// Drivers/typed layer call this before publishing.
func InjectContext(ctx context.Context, m *Message) {
	if len(carriers) == 0 {
		return
	}
	if m.Headers == nil {
		m.Headers = map[string]string{}
	}
	for _, c := range carriers {
		c.Inject(ctx, m.Headers)
	}
}

// ExtractContext returns a context derived from ctx carrying every registered
// carrier's value read from m.Headers. Drivers/typed layer call this before
// dispatching a delivery to a handler.
func ExtractContext(ctx context.Context, m Message) context.Context {
	for _, c := range carriers {
		ctx = c.Extract(ctx, m.Headers)
	}
	return ctx
}
