package mw

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bluebeard63/bosun"
)

func runCorrelation(t *testing.T, req *http.Request) (seen string, rec *httptest.ResponseRecorder) {
	t.Helper()
	h := (&Correlation{}).Handle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = bosun.CorrelationID(r.Context())
	}))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return seen, rec
}

func TestCorrelationGeneratesAndEchoes(t *testing.T) {
	seen, rec := runCorrelation(t, httptest.NewRequest("GET", "/", nil))
	if seen == "" {
		t.Fatal("no correlation id attached to context")
	}
	if got := rec.Header().Get(bosun.HeaderCorrelationID); got != seen {
		t.Fatalf("response header %q != ctx id %q", got, seen)
	}
}

func TestCorrelationReusesInbound(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set(bosun.HeaderCorrelationID, "abc-123")
	seen, rec := runCorrelation(t, req)
	if seen != "abc-123" {
		t.Fatalf("did not reuse inbound id: %q", seen)
	}
	if rec.Header().Get(bosun.HeaderCorrelationID) != "abc-123" {
		t.Fatal("did not echo inbound id")
	}
}

func TestCorrelationFromTraceparent(t *testing.T) {
	trace := strings.Repeat("a", 32)
	tp := "00-" + trace + "-" + strings.Repeat("b", 16) + "-01"
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set(bosun.HeaderTraceparent, tp)
	seen, _ := runCorrelation(t, req)
	if seen != trace {
		t.Fatalf("did not seed id from traceparent: %q", seen)
	}
}

func TestCorrelationReplacesInvalidInbound(t *testing.T) {
	for _, bad := range []string{
		strings.Repeat("a", 8<<10), // 8 KB
		strings.Repeat("a", 129),
		"abc-é", // non-ASCII
		"abc 123",
		`abc","admin":true`,
	} {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set(bosun.HeaderCorrelationID, bad)
		seen, rec := runCorrelation(t, req)
		if seen == bad || !validCorrelationID(seen) {
			t.Errorf("invalid inbound id kept: %.40q", seen)
		}
		if got := rec.Header().Get(bosun.HeaderCorrelationID); got != seen {
			t.Errorf("echoed %.40q, ctx %.40q", got, seen)
		}
	}

	ok := strings.Repeat("A", 123) + "z9._-"
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set(bosun.HeaderCorrelationID, ok)
	if seen, _ := runCorrelation(t, req); seen != ok {
		t.Fatalf("valid 128-char id replaced: %q", seen)
	}
}

func TestCorrelationIDFromTraceparentValidates(t *testing.T) {
	trace := "4bf92f3577b34da6a3ce929d0e0e4736"
	parent := "00f067aa0ba902b7"
	zeroTrace, zeroParent := strings.Repeat("0", 32), strings.Repeat("0", 16)
	for _, c := range []struct{ tp, want string }{
		{"00-" + trace + "-" + parent + "-01", trace},
		{"01-" + trace + "-" + parent + "-01-future", trace}, // later versions may append fields
		{"00-" + trace + "-" + parent + "-01-extra", ""},     // version 00 has exactly four fields
		{"00-" + zeroTrace + "-" + parent + "-01", ""},
		{"00-" + trace + "-" + zeroParent + "-01", ""},
		{"00-" + strings.Repeat("g", 32) + "-" + parent + "-01", ""},
		{"00-" + strings.ToUpper(trace) + "-" + parent + "-01", ""},
		{"ff-" + trace + "-" + parent + "-01", ""},
		{"0x-" + trace + "-" + parent + "-01", ""},
		{"00-" + trace + "-" + parent + "-zz", ""},
		{"00-" + trace + "-" + parent, ""},
		{"00_" + trace + "-" + parent + "-01", ""},
		{"", ""},
	} {
		if got := bosun.CorrelationIDFromTraceparent(c.tp); got != c.want {
			t.Errorf("CorrelationIDFromTraceparent(%q) = %q, want %q", c.tp, got, c.want)
		}
	}
}
