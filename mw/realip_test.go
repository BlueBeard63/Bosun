package mw

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bluebeard63/bosun"
)

func realIPHandler(t *testing.T, opts RealIPOptions) (http.Handler, *string) {
	t.Helper()
	seen := new(string)
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { *seen = ClientIP(r) })
	return (&RealIP{}).Configure(opts).Handle(next), seen
}

func TestRealIPResolution(t *testing.T) {
	trusted := RealIPOptions{TrustedProxies: []string{"10.0.0.0/8", "192.168.1.1", "fd00::/8"}}
	cases := []struct {
		name   string
		opts   RealIPOptions
		remote string
		hdr    map[string][]string
		want   string
	}{
		{"no proxies trusted ignores XFF", RealIPOptions{}, "10.0.0.1:5000",
			map[string][]string{"X-Forwarded-For": {"9.9.9.9"}}, "10.0.0.1"},
		{"untrusted peer ignores XFF", trusted, "8.8.8.8:5000",
			map[string][]string{"X-Forwarded-For": {"9.9.9.9"}}, "8.8.8.8"},
		{"trusted peer, single hop", trusted, "10.0.0.1:5000",
			map[string][]string{"X-Forwarded-For": {"9.9.9.9"}}, "9.9.9.9"},
		{"right-to-left skips trusted, ignores spoofed left", trusted, "10.0.0.1:5000",
			map[string][]string{"X-Forwarded-For": {"1.1.1.1, 9.9.9.9, 192.168.1.1, 10.2.3.4"}}, "9.9.9.9"},
		{"multiple XFF header lines", trusted, "10.0.0.1:5000",
			map[string][]string{"X-Forwarded-For": {"1.1.1.1", "9.9.9.9, 10.0.0.2"}}, "9.9.9.9"},
		{"all hops trusted takes leftmost", trusted, "10.0.0.1:5000",
			map[string][]string{"X-Forwarded-For": {"10.0.0.3, 10.0.0.2"}}, "10.0.0.3"},
		{"garbage hop stops at reporting proxy", trusted, "10.0.0.1:5000",
			map[string][]string{"X-Forwarded-For": {"9.9.9.9, nonsense, 10.0.0.2"}}, "10.0.0.2"},
		{"XFF with port", trusted, "10.0.0.1:5000",
			map[string][]string{"X-Forwarded-For": {"9.9.9.9:1234"}}, "9.9.9.9"},
		{"Forwarded header", trusted, "10.0.0.1:5000",
			map[string][]string{"Forwarded": {`for=1.1.1.1, for="[2001:db8::1]:4711";proto=https, for=10.0.0.9`}}, "2001:db8::1"},
		{"X-Real-IP", trusted, "10.0.0.1:5000",
			map[string][]string{"X-Real-IP": {"9.9.9.9"}}, "9.9.9.9"},
		{"IPv6 trusted peer", trusted, "[fd00::1]:5000",
			map[string][]string{"X-Forwarded-For": {"2001:db8::7"}}, "2001:db8::7"},
		{"IPv4-mapped peer", trusted, "[::ffff:10.0.0.1]:5000",
			map[string][]string{"X-Forwarded-For": {"9.9.9.9"}}, "9.9.9.9"},
		{"trusted peer without headers", trusted, "10.0.0.1:5000", nil, "10.0.0.1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, seen := realIPHandler(t, tc.opts)
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = tc.remote
			for k, vs := range tc.hdr {
				for _, v := range vs {
					req.Header.Add(k, v)
				}
			}
			h.ServeHTTP(httptest.NewRecorder(), req)
			if *seen != tc.want {
				t.Fatalf("ClientIP = %q, want %q", *seen, tc.want)
			}
		})
	}
}

func TestRealIPLeavesRemoteAddr(t *testing.T) {
	var remote string
	h := (&RealIP{}).Configure(RealIPOptions{TrustedProxies: []string{"10.0.0.0/8"}}).Handle(
		http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { remote = r.RemoteAddr }))
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:5000"
	req.Header.Set("X-Forwarded-For", "9.9.9.9")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if remote != "10.0.0.1:5000" {
		t.Fatalf("RemoteAddr rewritten to %q", remote)
	}
}

func TestClientIPWithoutRealIP(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "1.2.3.4:99"
	req.Header.Set("X-Forwarded-For", "9.9.9.9")
	if got := ClientIP(req); got != "1.2.3.4" {
		t.Fatalf("ClientIP = %q", got)
	}
}

func TestRealIPConfigurePanicsOnBadCIDR(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	(&RealIP{}).Configure(RealIPOptions{TrustedProxies: []string{"10.0.0.0/99"}})
}

func TestRealIPDynamic(t *testing.T) {
	d := &bosun.Dynamic[RealIPOptions]{}
	d.Set(&RealIPOptions{})
	m := &RealIP{opts: d}
	var seen string
	h := m.Handle(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { seen = ClientIP(r) }))
	do := func() string {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "10.0.0.1:5000"
		req.Header.Set("X-Forwarded-For", "9.9.9.9")
		h.ServeHTTP(httptest.NewRecorder(), req)
		return seen
	}
	if got := do(); got != "10.0.0.1" {
		t.Fatalf("untrusted: %q", got)
	}
	d.Set(&RealIPOptions{TrustedProxies: []string{"10.0.0.0/8"}})
	if got := do(); got != "9.9.9.9" {
		t.Fatalf("after reload: %q", got)
	}
	d.Set(&RealIPOptions{TrustedProxies: []string{"bogus"}})
	if got := do(); got != "10.0.0.1" {
		t.Fatalf("invalid reload should trust nobody: %q", got)
	}
}

// Acceptance: RateLimit behind RealIP buckets by client IP, not the proxy.
func TestRateLimitBehindRealIP(t *testing.T) {
	d := &bosun.Dynamic[RateLimitOptions]{}
	d.Set(&RateLimitOptions{PerMinute: 1})
	rl := &RateLimit{opts: d}
	if err := rl.Init(); err != nil {
		t.Fatal(err)
	}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	h := (&RealIP{}).Configure(RealIPOptions{TrustedProxies: []string{"10.0.0.0/8"}}).Handle(rl.Handle(ok))
	hit := func(client string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "10.0.0.1:5000" // always the load balancer
		req.Header.Set("X-Forwarded-For", client)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if c := hit("9.9.9.9").Code; c != 200 {
		t.Fatalf("first client: %d", c)
	}
	if c := hit("8.8.8.8").Code; c != 200 {
		t.Fatalf("second client shares the balancer's bucket: %d", c)
	}
	rec := hit("9.9.9.9")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("repeat client not limited: %d", rec.Code)
	}
	if ra := rec.Header().Get("Retry-After"); ra == "" || ra == "0" {
		t.Fatalf("Retry-After = %q", ra)
	}
}

// Acceptance: Logging lines include correlation_id (and client_ip behind RealIP).
func TestLoggingIncludesCorrelationAndClientIP(t *testing.T) {
	var buf bytes.Buffer
	lg := &Logging{log: slog.New(slog.NewJSONHandler(&buf, nil))}
	if err := lg.Init(); err != nil {
		t.Fatal(err)
	}
	h := (&RealIP{}).Configure(RealIPOptions{TrustedProxies: []string{"10.0.0.0/8"}}).Handle(
		(&Correlation{}).Handle(lg.Handle(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))))
	req := httptest.NewRequest("GET", "/x", nil)
	req.RemoteAddr = "10.0.0.1:5000"
	req.Header.Set("X-Forwarded-For", "9.9.9.9")
	req.Header.Set(bosun.HeaderCorrelationID, "corr-42")
	h.ServeHTTP(httptest.NewRecorder(), req)

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("log not JSON: %q", buf.String())
	}
	if line["correlation_id"] != "corr-42" || line["client_ip"] != "9.9.9.9" || line["remote"] != "10.0.0.1:5000" {
		t.Fatalf("log line = %v", line)
	}
}

func TestLoggingCorrelationFallbackToResponseHeader(t *testing.T) {
	var buf bytes.Buffer
	lg := &Logging{log: slog.New(slog.NewJSONHandler(&buf, nil))}
	_ = lg.Init()
	// Correlation inside Logging: the id is only visible on the response.
	h := lg.Handle((&Correlation{}).Handle(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	if id := rec.Header().Get(bosun.HeaderCorrelationID); id == "" || line["correlation_id"] != id {
		t.Fatalf("correlation_id = %v, header = %q", line["correlation_id"], id)
	}
	if _, ok := line["client_ip"]; ok {
		t.Fatal("client_ip logged without RealIP")
	}
}
