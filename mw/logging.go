// Package mw ships bosun's built-in middleware. Importing it makes the
// types available; attach them with bosun.Use[mw.Logging]() etc.
package mw

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/bluebeard63/bosun"
)

// --- Logging ---

// Logging writes one slog line per request ("request", at Info level, via
// InfoContext so context-aware handlers see the request context) with the
// method, path, status, duration, remote address (the TCP peer), the
// correlation id, and — when mw.RealIP ran upstream — the resolved
// client_ip.
//
// The correlation id is read from the request context, so register
// mw.Correlation outside Logging; if it runs inside instead, Logging falls
// back to the X-Correlation-ID response header it sets.
type Logging struct {
	log *slog.Logger
}

func (m *Logging) Init() error {
	if m.log == nil {
		m.log = slog.Default()
	}
	return nil
}

func (m *Logging) Handle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		cid := bosun.CorrelationID(r.Context())
		if cid == "" {
			cid = w.Header().Get(bosun.HeaderCorrelationID)
		}
		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", time.Since(start).String(),
			"remote", r.RemoteAddr,
			"correlation_id", cid,
		}
		if ip := resolvedClientIP(r); ip != "" {
			attrs = append(attrs, "client_ip", ip)
		}
		m.log.InfoContext(r.Context(), "request", attrs...)
	})
}

var _ = bosun.Middleware[Logging]()

// statusRecorder captures the response status for the log line. It forwards
// Flush and supports http.ResponseController via Unwrap, so streaming
// handlers (SSE, chunked) keep working behind Logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
