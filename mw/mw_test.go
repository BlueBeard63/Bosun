package mw

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/amberstack/bosun"
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
