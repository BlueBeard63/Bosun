package mw

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bluebeard63/bosun"
)

// The tests use either a generous deadline that a fast handler never hits,
// or a tiny one that the handler waits out on <-ctx.Done(); neither depends
// on scheduling luck.

func timeoutHandler(d time.Duration, h http.HandlerFunc) http.Handler {
	return (&Timeout{}).Configure(TimeoutOptions{Duration: d}).Handle(h)
}

func TestTimeoutFastHandlerUnaffected(t *testing.T) {
	var hasDeadline bool
	h := timeoutHandler(time.Minute, func(w http.ResponseWriter, r *http.Request) {
		_, hasDeadline = r.Context().Deadline()
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "ok")
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("Flush: %v", err)
		}
	})
	rec := serve(h, httptest.NewRequest("GET", "/", nil))
	if !hasDeadline || rec.Code != http.StatusCreated || rec.Body.String() != "ok" || !rec.Flushed {
		t.Fatalf("deadline = %v status = %d body = %q flushed = %v", hasDeadline, rec.Code, rec.Body, rec.Flushed)
	}
}

func TestTimeoutLateResponseReplacedBy503(t *testing.T) {
	var writeErr error
	h := timeoutHandler(time.Millisecond, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		w.Header().Set("Set-Cookie", "late=1")
		w.Header().Set("Content-Type", "text/plain")
		_, writeErr = io.WriteString(w, "too late")
	})
	rec := httptest.NewRecorder()
	rec.Header().Set(bosun.HeaderCorrelationID, "corr-1")
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	assert503(t, rec)
	if !errors.Is(writeErr, http.ErrHandlerTimeout) {
		t.Fatalf("late Write err = %v", writeErr)
	}
	if rec.Header().Get("Set-Cookie") != "" || rec.Header().Get(bosun.HeaderCorrelationID) != "corr-1" {
		t.Fatalf("headers = %v", rec.Header())
	}
}

func TestTimeoutHandlerReturnsWithoutWriting(t *testing.T) {
	h := timeoutHandler(time.Millisecond, func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // e.g. a DB call aborted by ctx, error swallowed
	})
	assert503(t, serve(h, httptest.NewRequest("GET", "/", nil)))
}

// expirableCtx lets a test decide exactly when the deadline has passed.
type expirableCtx struct {
	context.Context
	err error
}

func (c *expirableCtx) Err() error { return c.err }

func TestTimeoutStartedResponseLeftAlone(t *testing.T) {
	ctx := &expirableCtx{Context: context.Background()}
	rec := httptest.NewRecorder()
	tw := &timeoutWriter{ResponseWriter: rec, ctx: ctx}
	_, _ = io.WriteString(tw, "part1 ")
	ctx.err = context.DeadlineExceeded
	if _, err := io.WriteString(tw, "part2"); err != nil {
		t.Fatalf("write after deadline on a started response: %v", err)
	}
	if rec.Code != 200 || rec.Body.String() != "part1 part2" || tw.timedOut {
		t.Fatalf("status = %d body = %q", rec.Code, rec.Body)
	}
}

func TestTimeoutClientCancelIsNot503(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := timeoutHandler(time.Minute, func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	rec := serve(h, httptest.NewRequest("GET", "/", nil).WithContext(ctx))
	if rec.Code == http.StatusServiceUnavailable || rec.Body.Len() != 0 {
		t.Fatalf("client cancellation produced %d %q", rec.Code, rec.Body)
	}
}

func TestTimeoutDynamicAndDisable(t *testing.T) {
	d := &bosun.Dynamic[TimeoutOptions]{}
	d.Set(&TimeoutOptions{Duration: -1})
	var hasDeadline bool
	h := (&Timeout{opts: d}).Handle(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, hasDeadline = r.Context().Deadline()
	}))
	serve(h, httptest.NewRequest("GET", "/", nil))
	if hasDeadline {
		t.Fatal("negative duration should disable the deadline")
	}
	d.Set(&TimeoutOptions{Duration: time.Hour})
	serve(h, httptest.NewRequest("GET", "/", nil))
	if !hasDeadline {
		t.Fatal("hot-reloaded duration not applied")
	}
}

func TestTimeoutForwardsHijack(t *testing.T) {
	srv := httptest.NewServer(timeoutHandler(time.Minute, func(w http.ResponseWriter, _ *http.Request) {
		conn, bufrw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("Hijack: %v", err)
			return
		}
		defer conn.Close()
		_, _ = bufrw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 6\r\nConnection: close\r\n\r\nraw ok")
		_ = bufrw.Flush()
	}))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if b, _ := io.ReadAll(resp.Body); string(b) != "raw ok" {
		t.Fatalf("body = %q", b)
	}
}

func assert503(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["error"] != "request timed out" {
		t.Fatalf("body = %q (%v)", rec.Body, err)
	}
}
