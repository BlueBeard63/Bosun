package tracemod

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeSpan struct {
	ended bool
	err   error
}

func (s *fakeSpan) End()             { s.ended = true }
func (s *fakeSpan) SetError(e error) { s.err = e }

type fakeTracer struct {
	httpCalls int
	last      *fakeSpan
}

func (f *fakeTracer) StartHTTP(ctx context.Context, _, _ string) (context.Context, Span) {
	f.httpCalls++
	f.last = &fakeSpan{}
	return ctx, f.last
}
func (f *fakeTracer) StartConsume(ctx context.Context, _ string) (context.Context, Span) {
	f.last = &fakeSpan{}
	return ctx, f.last
}

func TestTracingMiddlewareStartsAndEnds(t *testing.T) {
	ft := &fakeTracer{}
	h := (&Tracing{Tracer: ft}).Handle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))

	if ft.httpCalls != 1 {
		t.Fatalf("StartHTTP called %d times, want 1", ft.httpCalls)
	}
	if !ft.last.ended {
		t.Fatal("span was not ended")
	}
	if ft.last.err != nil {
		t.Fatalf("unexpected error recorded: %v", ft.last.err)
	}
}

func TestTracingMiddlewareRecordsServerError(t *testing.T) {
	ft := &fakeTracer{}
	h := (&Tracing{Tracer: ft}).Handle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))

	if ft.last.err == nil {
		t.Fatal("expected a 5xx to record an error on the span")
	}
}

func TestNoopTracerDoesNotPanic(t *testing.T) {
	ctx, span := noop{}.StartHTTP(context.Background(), "GET", "/x")
	span.SetError(nil)
	span.End()
	_ = ctx
}
