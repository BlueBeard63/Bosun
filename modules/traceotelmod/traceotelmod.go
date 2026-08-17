// Package traceotelmod implements tracemod.Tracer over OpenTelemetry. The heavy
// OTel dependency lives only in this module; core Bosun and tracemod stay
// dependency-free. Set up your OTel TracerProvider and exporter in main, then
// install this tracer so the tracemod.Tracing middleware and any StartConsume
// calls emit real spans:
//
//	// configure OTel (stdout, OTLP, ...) and set the global provider, then:
//	traceotelmod.Install(app.Reg)
//
// Install uses registry.Register, so it overrides tracemod's default no-op
// tracer. Traces continue across HTTP and the event bus via the W3C traceparent
// that core Bosun carries on the context.
package traceotelmod

import (
	"context"

	"github.com/amberstack/bosun"
	"github.com/amberstack/bosun/modules/tracemod"
	"github.com/amberstack/bosun/registry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const scope = "github.com/amberstack/bosun"

type otelTracer struct {
	tr   trace.Tracer
	prop propagation.TextMapPropagator
}

var _ tracemod.Tracer = (*otelTracer)(nil)

func newTracer() *otelTracer {
	return &otelTracer{tr: otel.Tracer(scope), prop: propagation.TraceContext{}}
}

func (t *otelTracer) StartHTTP(ctx context.Context, method, path string) (context.Context, tracemod.Span) {
	return t.start(ctx, method+" "+path)
}

func (t *otelTracer) StartConsume(ctx context.Context, subject string) (context.Context, tracemod.Span) {
	return t.start(ctx, "consume "+subject)
}

// start continues any remote trace carried on ctx, opens a span, and writes the
// resulting traceparent back onto ctx for downstream propagation.
func (t *otelTracer) start(ctx context.Context, name string) (context.Context, tracemod.Span) {
	if tp := bosun.Traceparent(ctx); tp != "" {
		ctx = t.prop.Extract(ctx, propagation.MapCarrier{"traceparent": tp})
	}
	ctx, span := t.tr.Start(ctx, name)
	carrier := propagation.MapCarrier{}
	t.prop.Inject(ctx, carrier)
	if tp := carrier["traceparent"]; tp != "" {
		ctx = bosun.WithTraceparent(ctx, tp)
	}
	return ctx, &otelSpan{span: span}
}

type otelSpan struct{ span trace.Span }

func (s *otelSpan) End() { s.span.End() }

func (s *otelSpan) SetError(err error) {
	if err == nil {
		return
	}
	s.span.RecordError(err)
	s.span.SetStatus(codes.Error, err.Error())
}

// Install binds the OpenTelemetry tracer as the tracemod.Tracer on reg,
// overriding the default no-op. Call it in main.
func Install(reg *registry.Registry) {
	registry.Register[tracemod.Tracer](reg, func(*registry.Registry) (tracemod.Tracer, error) {
		return newTracer(), nil
	})
}
