package tracemod

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestTracingMiddlewareFlushes(t *testing.T) {
	ft := &fakeTracer{}
	var flushErr error
	h := (&Tracing{Tracer: ft}).Handle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: hi\n\n"))
		flushErr = http.NewResponseController(w).Flush()
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/events", nil))

	if flushErr != nil || !rec.Flushed {
		t.Fatalf("flush did not reach the underlying writer: %v", flushErr)
	}
	var w http.ResponseWriter = &statusRec{}
	if _, ok := w.(http.Flusher); !ok {
		t.Fatal("statusRec does not implement http.Flusher")
	}
	if _, ok := w.(http.Hijacker); !ok {
		t.Fatal("statusRec does not implement http.Hijacker")
	}
}

func TestTracingMiddlewareHijack(t *testing.T) {
	ft := &fakeTracer{}
	h := (&Tracing{Tracer: ft}).Handle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok"))
		_ = conn.Close()
	}))
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "ok" {
		t.Fatalf("body = %q", body)
	}
}

func TestTracingMiddlewareEndsSpanOnPanic(t *testing.T) {
	ft := &fakeTracer{}
	h := (&Tracing{Tracer: ft}).Handle(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	defer func() {
		if rv := recover(); rv != "boom" {
			t.Fatalf("recovered %v, want the original panic re-raised", rv)
		}
		if !ft.last.ended {
			t.Fatal("span was not ended after panic")
		}
		if ft.last.err == nil || !strings.Contains(ft.last.err.Error(), "boom") {
			t.Fatalf("panic not recorded on span: %v", ft.last.err)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
	t.Fatal("expected panic to propagate")
}

func TestNoopTracerDoesNotPanic(t *testing.T) {
	ctx, span := noop{}.StartHTTP(context.Background(), "GET", "/x")
	span.SetError(nil)
	span.End()
	_ = ctx
}
