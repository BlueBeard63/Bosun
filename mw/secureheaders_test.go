package mw

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bluebeard63/bosun"
)

func TestSecureHeadersDefaults(t *testing.T) {
	h := (&SecureHeaders{}).Configure(DefaultSecureHeaders()).Handle(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	rec := serve(h, httptest.NewRequest("GET", "/", nil))
	want := map[string]string{
		"X-Content-Type-Options":     "nosniff",
		"Strict-Transport-Security":  "max-age=31536000",
		"X-Frame-Options":            "DENY",
		"Referrer-Policy":            "strict-origin-when-cross-origin",
		"Cross-Origin-Opener-Policy": "same-origin",
		"Content-Security-Policy":    "",
		"Permissions-Policy":         "",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestSecureHeadersCustomAndOmitted(t *testing.T) {
	opts := SecureHeadersOptions{
		HSTSMaxAge:            time.Hour,
		HSTSIncludeSubdomains: true,
		HSTSPreload:           true,
		ContentSecurityPolicy: "default-src 'none'",
		PermissionsPolicy:     "camera=()",
	}
	h := (&SecureHeaders{}).Configure(opts).Handle(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	rec := serve(h, httptest.NewRequest("GET", "/", nil)) // handler writes nothing
	if got := rec.Header().Get("Strict-Transport-Security"); got != "max-age=3600; includeSubDomains; preload" {
		t.Fatalf("HSTS = %q", got)
	}
	if rec.Header().Get("Content-Security-Policy") != "default-src 'none'" ||
		rec.Header().Get("Permissions-Policy") != "camera=()" ||
		rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers = %v", rec.Header())
	}
	if _, ok := rec.Header()["X-Frame-Options"]; ok {
		t.Fatal("empty FrameOptions should omit the header")
	}
}

func TestSecureHeadersHandlerOverrides(t *testing.T) {
	h := (&SecureHeaders{}).Configure(DefaultSecureHeaders()).Handle(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Frame-Options", "SAMEORIGIN")
			w.WriteHeader(http.StatusAccepted)
		}))
	rec := serve(h, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusAccepted || rec.Header().Get("X-Frame-Options") != "SAMEORIGIN" {
		t.Fatalf("status = %d XFO = %q", rec.Code, rec.Header().Get("X-Frame-Options"))
	}
}

func TestSecureHeadersSurviveRecover(t *testing.T) {
	rcv, _ := newRecover(t)
	h := (&SecureHeaders{}).Configure(DefaultSecureHeaders()).Handle(rcv.Handle(panicking("boom")))
	rec := serve(h, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusInternalServerError || rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("status = %d headers = %v", rec.Code, rec.Header())
	}
}

func TestSecureHeadersFlushAndUnwrap(t *testing.T) {
	h := (&SecureHeaders{}).Configure(DefaultSecureHeaders()).Handle(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			if err := http.NewResponseController(w).Flush(); err != nil {
				t.Errorf("Flush: %v", err)
			}
			if _, ok := w.(interface{ Unwrap() http.ResponseWriter }); !ok {
				t.Error("no Unwrap")
			}
		}))
	rec := serve(h, httptest.NewRequest("GET", "/", nil))
	if !rec.Flushed || rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("flushed = %v headers = %v", rec.Flushed, rec.Header())
	}
}

func TestSecureHeadersDynamic(t *testing.T) {
	d := &bosun.Dynamic[SecureHeadersOptions]{}
	d.Set(&SecureHeadersOptions{})
	h := (&SecureHeaders{opts: d}).Handle(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if rec := serve(h, httptest.NewRequest("GET", "/", nil)); rec.Header().Get("X-Frame-Options") != "" {
		t.Fatal("zero options should only send nosniff")
	}
	d.Set(&SecureHeadersOptions{FrameOptions: "SAMEORIGIN"})
	if rec := serve(h, httptest.NewRequest("GET", "/", nil)); rec.Header().Get("X-Frame-Options") != "SAMEORIGIN" {
		t.Fatal("hot reload not applied")
	}
}
