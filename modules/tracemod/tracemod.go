// Package tracemod is the driver-agnostic tracing extension point. Core Bosun
// and the event bus start spans through the tracemod.Tracer interface; the
// default binding is a no-op, so tracing code paths are identical whether or
// not a real tracer is installed. Import a driver (e.g.
// github.com/bluebeard63/bosun/modules/traceotelmod) to send spans to
// OpenTelemetry — the heavy OTel dependency lives only in that driver module,
// never in core.
//
//	import _ "github.com/bluebeard63/bosun/modules/traceotelmod"
//	var _ = traceotelmod.Use()
//
// A service that wants spans injects a tracemod.Tracer:
//
//	type Worker struct{ Tracer tracemod.Tracer }
//	func (w *Worker) do(ctx context.Context) {
//	    ctx, span := w.Tracer.StartConsume(ctx, "orders.created")
//	    defer span.End()
//	    // ...
//	}
package tracemod

import (
	"context"

	"github.com/bluebeard63/bosun"
)

// Span is an in-progress unit of work. End finishes it; SetError records a
// failure on it.
type Span interface {
	End()
	SetError(err error)
}

// Tracer starts spans for the two boundaries Bosun instruments: inbound HTTP
// requests and event-queue consumption. Both return a context carrying the new
// span so nested work links up.
type Tracer interface {
	StartHTTP(ctx context.Context, method, path string) (context.Context, Span)
	StartConsume(ctx context.Context, subject string) (context.Context, Span)
}

// --- no-op default ---

type noopSpan struct{}

func (noopSpan) End()           {}
func (noopSpan) SetError(error) {}

type noop struct{}

func (noop) StartHTTP(ctx context.Context, _, _ string) (context.Context, Span) {
	return ctx, noopSpan{}
}
func (noop) StartConsume(ctx context.Context, _ string) (context.Context, Span) {
	return ctx, noopSpan{}
}

var _ = bosun.Service[noop]()
var _ = bosun.DefaultBind[Tracer, noop]()
