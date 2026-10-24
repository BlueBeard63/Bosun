package tracemod

import (
	"bufio"
	"fmt"
	"net"
	"net/http"

	"github.com/bluebeard63/bosun"
)

// Tracing starts a span for each request through the bound Tracer and ends it
// when the request returns, recording an error for 5xx responses and panics
// (which are re-raised for Recover to handle). It reads an
// inbound W3C traceparent so a span continues a remote trace, and leaves the
// resulting traceparent on the request context so downstream event publishes
// carry it. With the default no-op Tracer it costs nothing; install a driver
// (traceotelmod) to emit real spans.
//
//	var _ = bosun.Controller[API]("/api", bosun.Use[tracemod.Tracing]())
type Tracing struct {
	Tracer Tracer // injected
}

func (m *Tracing) Handle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if tp := r.Header.Get(bosun.HeaderTraceparent); tp != "" {
			ctx = bosun.WithTraceparent(ctx, tp)
		}
		ctx, span := m.Tracer.StartHTTP(ctx, r.Method, r.URL.Path)
		defer span.End()
		rec := &statusRec{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			// Record a panic on the span (ended by the defer above), then
			// let it continue to Recover or net/http.
			if rv := recover(); rv != nil {
				span.SetError(fmt.Errorf("panic: %v", rv))
				panic(rv)
			}
		}()
		next.ServeHTTP(rec, r.WithContext(ctx))
		if rec.status >= 500 {
			span.SetError(fmt.Errorf("status %d", rec.status))
		}
	})
}

var _ = bosun.Middleware[Tracing]()

// statusRec captures the response status for the span. It forwards Flush and
// Hijack and supports http.ResponseController via Unwrap, so SSE and
// WebSocket handlers keep working behind Tracing.
type statusRec struct {
	http.ResponseWriter
	status int
}

func (r *statusRec) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRec) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRec) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return h.Hijack()
}

func (r *statusRec) Unwrap() http.ResponseWriter { return r.ResponseWriter }
