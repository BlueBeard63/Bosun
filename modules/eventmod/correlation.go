package eventmod

import (
	"context"

	"github.com/amberstack/bosun"
)

// correlationCarrier propagates the request correlation id (and, when present,
// the W3C traceparent) across the event boundary so a trace begun over HTTP
// survives publish -> consume. It is registered automatically when eventmod is
// imported; when no correlation middleware ran the id is empty and nothing is
// copied. The context keys live in core bosun so tracing drivers can share them
// without eventmod depending on any tracing library.
type correlationCarrier struct{}

func (correlationCarrier) Inject(ctx context.Context, h map[string]string) {
	if id := bosun.CorrelationID(ctx); id != "" {
		h[bosun.HeaderCorrelationID] = id
	}
	if tp := bosun.Traceparent(ctx); tp != "" {
		h[bosun.HeaderTraceparent] = tp
	}
}

func (correlationCarrier) Extract(ctx context.Context, h map[string]string) context.Context {
	if id := h[bosun.HeaderCorrelationID]; id != "" {
		ctx = bosun.WithCorrelationID(ctx, id)
	}
	if tp := h[bosun.HeaderTraceparent]; tp != "" {
		ctx = bosun.WithTraceparent(ctx, tp)
	}
	return ctx
}

var _ = RegisterCarrier(correlationCarrier{})
