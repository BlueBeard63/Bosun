package mw

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bluebeard63/bosun"
)

func newRecover(t *testing.T) (*Recover, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	m := &Recover{log: slog.New(slog.NewJSONHandler(&buf, nil))}
	if err := m.Init(); err != nil {
		t.Fatal(err)
	}
	return m, &buf
}

func panicking(v any) http.Handler {
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(v) })
}

func TestRecoverHandlerPanic(t *testing.T) {
	m, logs := newRecover(t)
	rec := httptest.NewRecorder()
	m.Handle(panicking("db password is hunter2")).ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["error"] != "internal server error" {
		t.Fatalf("body = %q (%v)", rec.Body, err)
	}
	if strings.Contains(rec.Body.String(), "hunter2") || strings.Contains(rec.Body.String(), "goroutine") {
		t.Fatalf("panic details leaked to client: %s", rec.Body)
	}

	var entry map[string]any
	if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
		t.Fatalf("log not JSON: %v: %s", err, logs)
	}
	if entry["level"] != "ERROR" || entry["panic"] != "db password is hunter2" || entry["path"] != "/x" {
		t.Fatalf("log entry = %v", entry)
	}
	if stack, _ := entry["stack"].(string); !strings.Contains(stack, "goroutine") {
		t.Fatalf("stack missing from log: %v", entry["stack"])
	}
}

func TestRecoverDownstreamMiddlewarePanic(t *testing.T) {
	m, _ := newRecover(t)
	boom := func(http.Handler) http.Handler { return panicking(errors.New("mw broke")) }
	reached := false
	final := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })

	rec := httptest.NewRecorder()
	m.Handle(boom(final)).ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != http.StatusInternalServerError || reached {
		t.Fatalf("status = %d reached = %v", rec.Code, reached)
	}
}

func TestRecoverLogsCorrelationID(t *testing.T) {
	m, logs := newRecover(t)
	chain := (&Correlation{}).Handle(m.Handle(panicking("x")))
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set(bosun.HeaderCorrelationID, "corr-123")
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)

	if rec.Header().Get(bosun.HeaderCorrelationID) != "corr-123" {
		t.Fatal("correlation header lost on the 500 response")
	}
	if !strings.Contains(logs.String(), `"correlation_id":"corr-123"`) {
		t.Fatalf("correlation id missing from log: %s", logs)
	}
}

func TestRecoverAfterResponseStartedAborts(t *testing.T) {
	m, logs := newRecover(t)
	h := m.Handle(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		panic("late")
	}))

	defer func() {
		rv := recover()
		if rv != http.ErrAbortHandler {
			t.Fatalf("recovered %v, want http.ErrAbortHandler", rv)
		}
		if !strings.Contains(logs.String(), `"response_started":true`) {
			t.Fatalf("late panic not logged: %s", logs)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
	t.Fatal("expected abort panic")
}

func TestRecoverPassesThroughErrAbortHandler(t *testing.T) {
	m, logs := newRecover(t)
	defer func() {
		if rv := recover(); rv != http.ErrAbortHandler {
			t.Fatalf("recovered %v, want http.ErrAbortHandler", rv)
		}
		if logs.Len() != 0 {
			t.Fatalf("ErrAbortHandler should not be logged: %s", logs)
		}
	}()
	m.Handle(panicking(http.ErrAbortHandler)).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
}

func TestRecoverDropsHandlerContentHeaders(t *testing.T) {
	m, _ := newRecover(t)
	h := m.Handle(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Length", "999")
		panic("x")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Header().Get("Content-Type") != "application/json" || rec.Header().Get("Content-Length") != "" {
		t.Fatalf("headers = %v", rec.Header())
	}
}

func TestRecoverNoPanicIsTransparent(t *testing.T) {
	m, logs := newRecover(t)
	h := m.Handle(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, ok := w.(http.Flusher); !ok {
			t.Error("writer should still support Flush")
		}
		w.WriteHeader(http.StatusTeapot)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != http.StatusTeapot || logs.Len() != 0 {
		t.Fatalf("status = %d logs = %s", rec.Code, logs)
	}
}

// End to end: app-wide Recover around a typed handler that panics.
type recoverCtrl struct{}

var _ = bosun.Controller[recoverCtrl]("/recover-e2e")

func (c *recoverCtrl) Routes(r *bosun.Router) {
	bosun.Get(r, "/boom", func(_ context.Context, _ *bosun.Req[struct{}]) (struct{}, error) { panic("typed boom") })
}

func TestRecoverAppWideTypedHandler(t *testing.T) {
	app := bosun.New(bosun.WithMiddleware(bosun.Use[Recover]()))
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest("GET", "/recover-e2e/boom", nil))
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "internal server error") {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body)
	}
}
