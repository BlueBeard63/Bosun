package traceotelmod

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bluebeard63/bosun"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func setup(t *testing.T) (*tracetest.SpanRecorder, *sdktrace.TracerProvider) {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return rec, tp
}

func TestStartHTTPRecordsSpan(t *testing.T) {
	rec, tp := setup(t)
	tr := newTracer()
	ctx, span := tr.StartHTTP(context.Background(), "GET", "/users/1")
	span.End()
	_ = tp.ForceFlush(context.Background())

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	if spans[0].Name() != "GET /users/1" {
		t.Fatalf("span name = %q", spans[0].Name())
	}
	if bosun.Traceparent(ctx) == "" {
		t.Fatal("traceparent was not injected onto the context")
	}
}

func TestContinuesRemoteTrace(t *testing.T) {
	rec, tp := setup(t)
	tr := newTracer()
	traceID := strings.Repeat("a", 32)
	remote := "00-" + traceID + "-" + strings.Repeat("b", 16) + "-01"

	ctx := bosun.WithTraceparent(context.Background(), remote)
	_, span := tr.StartHTTP(ctx, "GET", "/x")
	span.End()
	_ = tp.ForceFlush(context.Background())

	got := rec.Ended()[0].SpanContext().TraceID().String()
	if got != traceID {
		t.Fatalf("child span trace id = %s, want %s (did not continue remote trace)", got, traceID)
	}
}

func TestSetErrorMarksSpan(t *testing.T) {
	rec, tp := setup(t)
	tr := newTracer()
	_, span := tr.StartConsume(context.Background(), "orders.created")
	span.SetError(errors.New("boom"))
	span.End()
	_ = tp.ForceFlush(context.Background())

	s := rec.Ended()[0]
	if s.Name() != "consume orders.created" {
		t.Fatalf("span name = %q", s.Name())
	}
	if s.Status().Code != codes.Error {
		t.Fatalf("status = %v, want Error", s.Status().Code)
	}
}
