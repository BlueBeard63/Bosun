package mw

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/bluebeard63/bosun"
)

// --- Timeout ---

// TimeoutOptions configures Timeout.
type TimeoutOptions struct {
	// Duration is the per-request deadline. Zero means 30s; a negative
	// value disables the middleware.
	Duration time.Duration
}

const defaultTimeout = 30 * time.Second

var _ = bosun.DefaultDynamic[TimeoutOptions](func() *TimeoutOptions {
	return &TimeoutOptions{Duration: defaultTimeout}
})

// timeoutBody is the JSON body of the 503, shaped like Recover's 500.
const timeoutBody = `{"error":"request timed out"}` + "\n"

// Timeout puts a deadline on each request's context. When the deadline
// passes before the handler has started its response, the client gets
// 503 Service Unavailable with {"error":"request timed out"}, and anything
// the handler writes afterwards is discarded (Write returns
// http.ErrHandlerTimeout). A response that started before the deadline is
// left alone: its status is already on the wire.
//
// Unlike http.TimeoutHandler, Timeout runs the handler on the request's own
// goroutine and never writes concurrently with it, so there is no race on
// the ResponseWriter and no full-response buffering, and Flush, Hijack and
// http.ResponseController (via Unwrap) keep working. The trade-off: the
// deadline is cooperative. Handlers must honour ctx (database calls, HTTP
// clients and bosun's typed handlers all do); a handler that ignores it
// runs to completion, and only then is its late response replaced by the
// 503.
//
// Streaming endpoints (SSE, WebSockets, long polling) should not sit behind
// Timeout, since the cancelled context will end the stream; register it on
// the controllers or groups that need it, or app-wide with care.
//
//	var _ = bosun.Controller[Reports]("/reports",
//	    bosun.Use[mw.Timeout](mw.TimeoutOptions{Duration: 5 * time.Second}))
//
// With no arguments Timeout reads the injected
// *bosun.Dynamic[TimeoutOptions] (default 30s) per request.
type Timeout struct {
	opts *bosun.Dynamic[TimeoutOptions] // injected, hot-reloadable
}

// Configure returns a Timeout handler with a fixed duration, used by
// bosun.Use[mw.Timeout](opts).
func (m *Timeout) Configure(opts TimeoutOptions) bosun.MiddlewareHandler {
	return bosun.MiddlewareFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serveTimeout(opts.Duration, w, r, next)
		})
	})
}

func (m *Timeout) Handle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var d time.Duration
		if m.opts != nil {
			if o := m.opts.Get(); o != nil {
				d = o.Duration
			}
		}
		serveTimeout(d, w, r, next)
	})
}

var _ = bosun.Middleware[Timeout]()

func serveTimeout(d time.Duration, w http.ResponseWriter, r *http.Request, next http.Handler) {
	if d < 0 {
		next.ServeHTTP(w, r)
		return
	}
	if d == 0 {
		d = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(r.Context(), d)
	defer cancel()
	tw := &timeoutWriter{ResponseWriter: w, ctx: ctx}
	next.ServeHTTP(tw, r.WithContext(ctx))
	if !tw.started && !tw.timedOut && tw.expired() {
		tw.timeout()
	}
}

// timeoutWriter swaps in the 503 when the handler's response would start
// after the deadline. All calls happen on the handler's goroutine.
type timeoutWriter struct {
	http.ResponseWriter
	ctx context.Context

	started  bool // response committed (or connection hijacked)
	timedOut bool // the 503 was written; the handler's output is discarded
}

func (w *timeoutWriter) expired() bool {
	return errors.Is(w.ctx.Err(), context.DeadlineExceeded)
}

func (w *timeoutWriter) timeout() {
	w.timedOut = true
	h := w.Header()
	for k := range h {
		if !keepOnPanic(k) {
			delete(h, k)
		}
	}
	h.Set("Content-Type", "application/json")
	h.Set("X-Content-Type-Options", "nosniff")
	w.ResponseWriter.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.ResponseWriter.Write([]byte(timeoutBody))
}

func (w *timeoutWriter) WriteHeader(code int) {
	switch {
	case w.timedOut:
		return
	case w.started:
		w.ResponseWriter.WriteHeader(code) // let net/http report the superfluous call
		return
	case code < 200 && code != http.StatusSwitchingProtocols:
		w.ResponseWriter.WriteHeader(code) // informational; doesn't commit
		return
	case w.expired():
		w.timeout()
		return
	}
	w.started = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *timeoutWriter) Write(b []byte) (int, error) {
	if !w.started && !w.timedOut {
		w.WriteHeader(http.StatusOK)
	}
	if w.timedOut {
		return 0, http.ErrHandlerTimeout
	}
	return w.ResponseWriter.Write(b)
}

func (w *timeoutWriter) Flush() {
	if !w.started && !w.timedOut {
		w.WriteHeader(http.StatusOK)
	}
	if w.timedOut {
		return
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *timeoutWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w.timedOut {
		return nil, nil, http.ErrHandlerTimeout
	}
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	conn, rw, err := h.Hijack()
	if err == nil {
		w.started = true
	}
	return conn, rw, err
}

func (w *timeoutWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
