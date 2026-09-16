package healthmod

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/bluebeard63/bosun"
)

// swapProbes clears the global probe slice for a test and restores it after, so
// probe registrations don't leak between tests.
func swapProbes(t *testing.T) {
	t.Helper()
	probeMu.Lock()
	saved := probes
	probes = nil
	probeMu.Unlock()
	t.Cleanup(func() {
		probeMu.Lock()
		probes = saved
		probeMu.Unlock()
	})
}

func newHealthApp(t *testing.T) *bosun.App {
	t.Helper()
	app := bosun.New(bosun.OverridePrefix[HealthController]("/h"))
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	return app
}

func get(t *testing.T, app *bosun.App, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

func TestReadyAllProbesPass(t *testing.T) {
	swapProbes(t)
	Register(ProbeFunc{N: "db", F: func(context.Context) error { return nil }})
	app := newHealthApp(t)

	rec := get(t, app, "/h/ready")
	if rec.Code != 200 {
		t.Fatalf("ready -> %d, want 200", rec.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["status"] != "ok" {
		t.Fatalf("status = %v", body["status"])
	}
}

func TestReadyFailingProbe(t *testing.T) {
	swapProbes(t)
	Register(ProbeFunc{N: "db", F: func(context.Context) error { return errors.New("connection refused") }})
	Register(ProbeFunc{N: "cache", F: func(context.Context) error { return nil }})
	app := newHealthApp(t)

	rec := get(t, app, "/h/ready")
	if rec.Code != 503 {
		t.Fatalf("ready -> %d, want 503", rec.Code)
	}
	var body struct {
		Status string            `json:"status"`
		Failed map[string]string `json:"failed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "unready" {
		t.Fatalf("status = %q", body.Status)
	}
	if body.Failed["db"] == "" {
		t.Fatalf("expected db failure reported, got %v", body.Failed)
	}
	if _, ok := body.Failed["cache"]; ok {
		t.Fatalf("healthy probe should not appear in failed: %v", body.Failed)
	}
}

func TestLiveAlwaysOK(t *testing.T) {
	swapProbes(t)
	Register(ProbeFunc{N: "db", F: func(context.Context) error { return errors.New("down") }})
	app := newHealthApp(t)
	// Liveness must not depend on probes.
	if rec := get(t, app, "/h/live"); rec.Code != 200 {
		t.Fatalf("live -> %d, want 200", rec.Code)
	}
}
