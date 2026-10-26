package mw

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bluebeard63/bosun"
)

func TestLogging(t *testing.T) {
	m := &Logging{}
	if err := m.Init(); err != nil {
		t.Fatal(err)
	}
	called := false
	h := m.Handle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if !called || rec.Code != http.StatusTeapot {
		t.Fatalf("handler not invoked or status lost: %d", rec.Code)
	}
}

func TestRateLimit(t *testing.T) {
	d := &bosun.Dynamic[RateLimitOptions]{}
	d.Set(&RateLimitOptions{PerMinute: 2})
	m := &RateLimit{opts: d}
	if err := m.Init(); err != nil {
		t.Fatal(err)
	}
	h := m.Handle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))

	codes := []int{}
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", "/x", nil)
		req.RemoteAddr = "1.2.3.4:1000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		codes = append(codes, rec.Code)
	}
	if codes[0] != 200 || codes[1] != 200 || codes[2] != http.StatusTooManyRequests {
		t.Fatalf("limiting wrong: %v", codes)
	}

	// other IPs have their own window
	req := httptest.NewRequest("GET", "/x", nil)
	req.RemoteAddr = "5.6.7.8:1000"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatal("per-IP isolation broken")
	}

	// hot reload: raising the limit unblocks the first IP
	d.Set(&RateLimitOptions{PerMinute: 100})
	req = httptest.NewRequest("GET", "/x", nil)
	req.RemoteAddr = "1.2.3.4:1000"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Header().Get("X-RateLimit-Limit") != "100" {
		t.Fatalf("dynamic limit not applied: %d %s", rec.Code, rec.Header().Get("X-RateLimit-Limit"))
	}
}

func TestRateLimitEvictsIdleKeys(t *testing.T) {
	d := &bosun.Dynamic[RateLimitOptions]{}
	d.Set(&RateLimitOptions{PerMinute: 10})
	now := time.Now()
	m := &RateLimit{opts: d, now: func() time.Time { return now }}
	if err := m.Init(); err != nil {
		t.Fatal(err)
	}
	h := m.Handle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	hit := func(addr string) {
		req := httptest.NewRequest("GET", "/x", nil)
		req.RemoteAddr = addr
		h.ServeHTTP(httptest.NewRecorder(), req)
	}

	for i := 0; i < 100; i++ {
		hit(fmt.Sprintf("10.0.0.%d:1000", i))
	}
	if n := len(m.windows); n != 100 {
		t.Fatalf("windows = %d, want 100", n)
	}

	now = now.Add(2 * time.Minute)
	hit("1.2.3.4:1000")
	if n := len(m.windows); n != 1 {
		t.Fatalf("idle keys not evicted: %d windows remain", n)
	}
}

func TestRateLimitRetryAfter(t *testing.T) {
	d := &bosun.Dynamic[RateLimitOptions]{}
	d.Set(&RateLimitOptions{PerMinute: 1})
	now := time.Now()
	m := &RateLimit{opts: d, now: func() time.Time { return now }}
	if err := m.Init(); err != nil {
		t.Fatal(err)
	}
	h := m.Handle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	hit := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/x", nil)
		req.RemoteAddr = "1.2.3.4:1000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := hit(); rec.Header().Get("Retry-After") != "" {
		t.Fatal("Retry-After on an allowed request")
	}
	now = now.Add(20*time.Second + 500*time.Millisecond)
	rec := hit()
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "40" {
		t.Fatalf("status = %d Retry-After = %q, want 429 / 40", rec.Code, rec.Header().Get("Retry-After"))
	}
}

func TestLoggingFlushes(t *testing.T) {
	m := &Logging{}
	if err := m.Init(); err != nil {
		t.Fatal(err)
	}
	var flushErr error
	h := m.Handle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: hi\n\n"))
		flushErr = http.NewResponseController(w).Flush()
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/events", nil))
	if flushErr != nil {
		t.Fatalf("ResponseController.Flush: %v", flushErr)
	}
	if !rec.Flushed {
		t.Fatal("flush did not reach the underlying writer")
	}
	if _, ok := any(&statusRecorder{}).(http.Flusher); !ok {
		t.Fatal("statusRecorder does not implement http.Flusher")
	}
}
