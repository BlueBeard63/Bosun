package mw

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/amberstack/bosun"
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
