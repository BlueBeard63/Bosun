package tracemod

import (
	"fmt"
	"net/http"

	"github.com/bluebeard63/bosun"
)

// Tracing starts a span for each request through the bound Tracer and ends it
// when the request returns, recording an error for 5xx responses. It reads an
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
		rec := &statusRec{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r.WithContext(ctx))
		if rec.status >= 500 {
			span.SetError(fmt.Errorf("status %d", rec.status))
		}
		span.End()
	})
}

var _ = bosun.Middleware[Tracing]()

type statusRec struct {
	http.ResponseWriter
	status int
}

func (r *statusRec) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
