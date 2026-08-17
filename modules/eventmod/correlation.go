package eventmod

import (
	"context"

	"github.com/amberstack/bosun"
)

// correlationCarrier propagates the request correlation id (and, when present,
// the W3C traceparent) across the event boundary so a trace begun over HTTP
// survives publish -> consume. It is registered automatically when eventmod is
// imported; when no correlation middleware ran the id is empty and nothing is
// copied.
type correlationCarrier struct{}

const headerTraceparent = "traceparent"

func (correlationCarrier) Inject(ctx context.Context, h map[string]string) {
	if id := bosun.CorrelationID(ctx); id != "" {
		h[bosun.HeaderCorrelationID] = id
	}
	if tp, ok := ctx.Value(traceparentKey{}).(string); ok && tp != "" {
		h[headerTraceparent] = tp
	}
}

func (correlationCarrier) Extract(ctx context.Context, h map[string]string) context.Context {
	if id := h[bosun.HeaderCorrelationID]; id != "" {
		ctx = bosun.WithCorrelationID(ctx, id)
	}
	if tp := h[headerTraceparent]; tp != "" {
		ctx = context.WithValue(ctx, traceparentKey{}, tp)
	}
	return ctx
}

// traceparentKey carries a raw W3C traceparent string on a context so tracing
// drivers (traceotelmod) can continue a remote trace across the bus without
// eventmod depending on OpenTelemetry.
type traceparentKey struct{}

// WithTraceparent attaches a raw W3C traceparent string to ctx.
func WithTraceparent(ctx context.Context, tp string) context.Context {
	return context.WithValue(ctx, traceparentKey{}, tp)
}

// Traceparent returns the raw W3C traceparent attached to ctx, or "".
func Traceparent(ctx context.Context) string {
	tp, _ := ctx.Value(traceparentKey{}).(string)
	return tp
}

var _ = RegisterCarrier(correlationCarrier{})
