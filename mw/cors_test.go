package mw

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bluebeard63/bosun"
)

const frontend = "http://localhost:5173"

func corsHandler(opts CORSOptions) (http.Handler, *bool) {
	reached := new(bool)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*reached = true
		w.Header().Set("X-Correlation-ID", "abc")
		w.WriteHeader(http.StatusOK)
	})
	return (&CORS{}).Configure(opts).Handle(next), reached
}

func corsReq(method, origin string, hdr map[string]string) *http.Request {
	r := httptest.NewRequest(method, "/api/things", nil)
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}

func preflight(origin, method, headers string) *http.Request {
	h := map[string]string{"Access-Control-Request-Method": method}
	if headers != "" {
		h["Access-Control-Request-Headers"] = headers
	}
	return corsReq("OPTIONS", origin, h)
}

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestCORSSimpleRequestAllowedOrigin(t *testing.T) {
	h, reached := corsHandler(CORSOptions{AllowedOrigins: []string{frontend}})
	rec := serve(h, corsReq("GET", frontend, nil))
	if !*reached || rec.Code != 200 {
		t.Fatalf("handler not reached: %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != frontend {
		t.Fatalf("Allow-Origin = %q", got)
	}
	if rec.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("credentials header set without AllowCredentials")
	}
	if !strings.Contains(strings.Join(rec.Header().Values("Vary"), ","), "Origin") {
		t.Fatal("missing Vary: Origin")
	}
}

func TestCORSDisallowedOrigin(t *testing.T) {
	h, reached := corsHandler(CORSOptions{AllowedOrigins: []string{frontend}})
	rec := serve(h, corsReq("GET", "https://evil.example", nil))
	if !*reached {
		t.Fatal("non-CORS handling should still reach the handler (browser enforces)")
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("disallowed origin got Allow-Origin")
	}

	rec = serve(h, preflight("https://evil.example", "PUT", ""))
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("disallowed preflight: %d %v", rec.Code, rec.Header())
	}
}

func TestCORSNoOriginPassesThrough(t *testing.T) {
	h, reached := corsHandler(CORSOptions{AllowedOrigins: []string{"*"}})
	rec := serve(h, corsReq("GET", "", nil))
	if !*reached || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("same-origin request altered: %v", rec.Header())
	}
}

func TestCORSPreflight(t *testing.T) {
	h, reached := corsHandler(CORSOptions{
		AllowedOrigins: []string{frontend},
		AllowedMethods: []string{"GET", "PUT"},
		AllowedHeaders: []string{"Content-Type", "X-Api-Key"},
		MaxAge:         10 * time.Minute,
	})
	rec := serve(h, preflight(frontend, "PUT", "content-type, x-api-key"))
	if *reached {
		t.Fatal("preflight must not reach the handler")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
	want := map[string]string{
		"Access-Control-Allow-Origin":  frontend,
		"Access-Control-Allow-Methods": "GET, PUT",
		"Access-Control-Allow-Headers": "content-type, x-api-key",
		"Access-Control-Max-Age":       "600",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestCORSPreflightRejectsMethodAndHeader(t *testing.T) {
	h, _ := corsHandler(CORSOptions{AllowedOrigins: []string{frontend}, AllowedMethods: []string{"GET"}})
	if rec := serve(h, preflight(frontend, "DELETE", "")); rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("disallowed method accepted")
	}
	if rec := serve(h, preflight(frontend, "GET", "X-Secret")); rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("disallowed header accepted")
	}
	// default headers include Content-Type and Authorization
	if rec := serve(h, preflight(frontend, "GET", "Authorization,Content-Type")); rec.Header().Get("Access-Control-Allow-Origin") != frontend {
		t.Fatal("default allowed headers rejected")
	}
}

func TestCORSAnyHeader(t *testing.T) {
	h, _ := corsHandler(CORSOptions{AllowedOrigins: []string{frontend}, AllowedHeaders: []string{"*"}})
	rec := serve(h, preflight(frontend, "POST", "X-Anything"))
	if rec.Header().Get("Access-Control-Allow-Headers") != "X-Anything" {
		t.Fatalf("headers = %v", rec.Header())
	}
}

func TestCORSWildcardOriginWithoutCredentials(t *testing.T) {
	h, _ := corsHandler(CORSOptions{AllowedOrigins: []string{"*"}})
	rec := serve(h, corsReq("GET", frontend, nil))
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("Allow-Origin = %q, want *", rec.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCORSCredentialsEchoOriginNeverStar(t *testing.T) {
	h, _ := corsHandler(CORSOptions{AllowedOrigins: []string{"*"}, AllowCredentials: true})
	for _, r := range []*http.Request{corsReq("GET", frontend, nil), preflight(frontend, "POST", "")} {
		rec := serve(h, r)
		if rec.Header().Get("Access-Control-Allow-Origin") != frontend {
			t.Fatalf("%s: Allow-Origin = %q, want echoed origin", r.Method, rec.Header().Get("Access-Control-Allow-Origin"))
		}
		if rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
			t.Fatalf("%s: missing Allow-Credentials", r.Method)
		}
	}
}

func TestCORSExposedHeaders(t *testing.T) {
	h, _ := corsHandler(CORSOptions{AllowedOrigins: []string{frontend}, ExposedHeaders: []string{"X-Correlation-ID", "X-Total"}})
	rec := serve(h, corsReq("GET", frontend, nil))
	if got := rec.Header().Get("Access-Control-Expose-Headers"); got != "X-Correlation-ID, X-Total" {
		t.Fatalf("Expose-Headers = %q", got)
	}
	if rec := serve(h, preflight(frontend, "GET", "")); rec.Header().Get("Access-Control-Expose-Headers") != "" {
		t.Fatal("Expose-Headers belongs on actual responses, not preflights")
	}
}

func TestCORSOriginMatching(t *testing.T) {
	h, _ := corsHandler(CORSOptions{
		AllowedOrigins:  []string{"https://*.example.com", "HTTPS://Exact.Test"},
		AllowOriginFunc: func(o string) bool { return o == "https://func.dev" },
	})
	cases := map[string]bool{
		"https://app.example.com":  true,
		"https://a.b.example.com":  true,
		"https://example.com":      false,
		"http://app.example.com":   false,
		"https://example.com.evil": false,
		"https://exact.test":       true,
		"https://func.dev":         true,
		"null":                     false,
		"https://notexample.com":   false,
	}
	for origin, want := range cases {
		got := serve(h, corsReq("GET", origin, nil)).Header().Get("Access-Control-Allow-Origin") != ""
		if got != want {
			t.Errorf("%s: allowed = %v, want %v", origin, got, want)
		}
	}
}

func TestCORSZeroValueAllowsNothing(t *testing.T) {
	h, _ := corsHandler(CORSOptions{})
	if rec := serve(h, corsReq("GET", frontend, nil)); rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("zero options should allow no origins")
	}
}

func TestCORSDynamicHotReload(t *testing.T) {
	d := &bosun.Dynamic[CORSOptions]{}
	d.Set(&CORSOptions{AllowedOrigins: []string{"https://old.dev"}})
	m := &CORS{opts: d}
	h := m.Handle(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	if serve(h, corsReq("GET", "https://new.dev", nil)).Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("new.dev allowed before reload")
	}
	d.Set(&CORSOptions{AllowedOrigins: []string{"https://new.dev"}})
	if serve(h, corsReq("GET", "https://new.dev", nil)).Header().Get("Access-Control-Allow-Origin") != "https://new.dev" {
		t.Fatal("reload not applied")
	}
}

// End to end: app-wide CORS answers a preflight for a route that only
// registers POST, and decorates the actual typed response.
type corsCtrl struct{}

var _ = bosun.Controller[corsCtrl]("/cors-e2e")

func (c *corsCtrl) Routes(r *bosun.Router) {
	bosun.Post(r, "/items", func(context.Context, *bosun.Req[struct{}]) (map[string]int, error) {
		return map[string]int{"id": 1}, nil
	})
}

func TestCORSAppWide(t *testing.T) {
	app := bosun.New(bosun.WithMiddleware(bosun.Use[CORS](CORSOptions{
		AllowedOrigins:   []string{frontend},
		AllowCredentials: true,
	})))
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("OPTIONS", "/cors-e2e/items", nil)
	req.Header.Set("Origin", frontend)
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Content-Type")
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != frontend {
		t.Fatalf("preflight: %d %v", rec.Code, rec.Header())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/cors-e2e/items", strings.NewReader(`{}`))
	req.Header.Set("Origin", frontend)
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("actual: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
}
