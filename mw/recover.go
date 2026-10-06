package mw

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"

	"github.com/bluebeard63/bosun"
)

// --- Recover ---

// Recover catches panics from downstream middleware and handlers, logs the
// panic value and stack trace through slog (with the request's correlation
// id), and responds with a JSON 500 — {"error":"internal server error"} —
// the same shape the typed adapter uses for unexpected errors. The panic
// value and stack never reach the client.
//
// If the response has already started (headers written) the status can no
// longer change, so Recover logs the panic and aborts the connection by
// re-panicking with http.ErrAbortHandler; the client sees a truncated
// response rather than a misleading success. A panic of http.ErrAbortHandler
// itself is passed through untouched.
//
// Register it app-wide, inside Correlation/Logging/Tracing so those layers
// still observe the 500 and the log line carries the correlation id:
//
//	app := bosun.New(bosun.WithMiddleware(
//	    bosun.Use[mw.Correlation](),
//	    bosun.Use[mw.Logging](),
//	    bosun.Use[mw.Recover](),
//	))
type Recover struct {
	log *slog.Logger
}

func (m *Recover) Init() error {
	if m.log == nil {
		m.log = slog.Default()
	}
	return nil
}

func (m *Recover) Handle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &recoverWriter{ResponseWriter: w}
		defer func() {
			rv := recover()
			if rv == nil {
				return
			}
			if err, ok := rv.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(rv)
			}
			m.logger().ErrorContext(r.Context(), "panic recovered",
				"panic", fmt.Sprint(rv),
				"method", r.Method,
				"path", r.URL.Path,
				"correlation_id", bosun.CorrelationID(r.Context()),
				"response_started", rw.started,
				"stack", string(debug.Stack()),
			)
			if rw.started {
				panic(http.ErrAbortHandler)
			}
			h := w.Header()
			h.Del("Content-Length")
			h.Set("Content-Type", "application/json")
			h.Set("X-Content-Type-Options", "nosniff")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"internal server error"}` + "\n"))
		}()
		next.ServeHTTP(rw, r)
	})
}

func (m *Recover) logger() *slog.Logger {
	if m.log == nil {
		return slog.Default()
	}
	return m.log
}

var _ = bosun.Middleware[Recover]()

// recoverWriter records whether the response has started so Recover knows
// if it can still write a 500. It forwards Flush and Hijack and supports
// http.ResponseController via Unwrap, so streaming handlers keep working.
type recoverWriter struct {
	http.ResponseWriter
	started bool
}

func (w *recoverWriter) WriteHeader(code int) {
	if code >= 200 || code == http.StatusSwitchingProtocols {
		w.started = true // 1xx informational headers don't commit the response
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *recoverWriter) Write(b []byte) (int, error) {
	w.started = true
	return w.ResponseWriter.Write(b)
}

func (w *recoverWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		w.started = true
		f.Flush()
	}
}

func (w *recoverWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	w.started = true
	return h.Hijack()
}

func (w *recoverWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
