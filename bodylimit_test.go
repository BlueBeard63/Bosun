package bosun

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type limitIn struct {
	Name string `json:"name" form:"name"`
}

var limitCalls atomic.Int64

type bodyLimitCtrl struct{}

var _ = Controller[bodyLimitCtrl]("/bodylimit")

func (c *bodyLimitCtrl) Routes(r *Router) {
	h := func(_ context.Context, req *Req[limitIn]) (int, error) {
		limitCalls.Add(1)
		return len(req.Body.Name), nil
	}
	Post(r, "/default", h)
	Post(r, "/big", h, MaxBody(4<<20))
	Post(r, "/small", h, MaxBody(16))
	Post(r, "/unlimited", h, MaxBody(0))
	Post(r, "/string", func(_ context.Context, req *Req[string]) (int, error) {
		limitCalls.Add(1)
		return len(req.Body), nil
	})
	// struct{} In: the handler owns the body, so only an explicit MaxBody caps it.
	Post(r, "/stream", func(_ context.Context, req *Req[struct{}]) (int, error) {
		limitCalls.Add(1)
		n, err := io.Copy(io.Discard, req.Request.Body)
		return int(n), err
	})
	Post(r, "/stream-capped", func(_ context.Context, req *Req[struct{}]) (int, error) {
		limitCalls.Add(1)
		return 0, nil
	}, MaxBody(16))
	r.Post("/raw", func(w http.ResponseWriter, req *http.Request) {
		n, err := io.Copy(io.Discard, req.Body)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_, _ = io.WriteString(w, strconv.FormatInt(n, 10))
	})
}

// jsonBody is a valid JSON object of about n bytes.
func jsonBody(n int) string {
	return `{"name":"` + strings.Repeat("a", n) + `"}`
}

// postLimit sends body to path; unknown length hides the size from the
// Content-Length check so the MaxBytesReader path is exercised.
func postLimit(app *App, path, ct, body string, unknownLength bool) *httptest.ResponseRecorder {
	var r io.Reader = strings.NewReader(body)
	if unknownLength {
		r = io.MultiReader(r)
	}
	req := httptest.NewRequest("POST", path, r)
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	return rec
}

func expect413(t *testing.T, rec *httptest.ResponseRecorder, what string) {
	t.Helper()
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("%s: status %d, want 413: %s", what, rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"request body too large"}` {
		t.Fatalf("%s: body %s", what, got)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("%s: content type %q", what, ct)
	}
}

func TestBodyLimitDefault(t *testing.T) {
	app := newStarted(t)
	big := jsonBody(int(DefaultMaxBody))
	for _, unknown := range []bool{false, true} {
		limitCalls.Store(0)
		expect413(t, postLimit(app, "/bodylimit/default", "application/json", big, unknown), "json")
		expect413(t, postLimit(app, "/bodylimit/string", "text/plain", big, unknown), "string")
		if n := limitCalls.Load(); n != 0 {
			t.Fatalf("handler called %d times for oversized bodies", n)
		}
	}
	if rec := postLimit(app, "/bodylimit/default", "application/json", jsonBody(1000), true); rec.Code != 200 {
		t.Fatalf("small body: %d %s", rec.Code, rec.Body.String())
	}
	if got := app.ObservedStatuses("POST", "/bodylimit/default"); len(got) != 2 {
		t.Fatalf("observed %v, want 200 and 413", got)
	}
}

func TestBodyLimitForms(t *testing.T) {
	app := newStarted(t, WithMaxBody(64))
	form := "name=" + strings.Repeat("a", 100)
	expect413(t, postLimit(app, "/bodylimit/default", "application/x-www-form-urlencoded", form, true), "urlencoded")

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("name", strings.Repeat("a", 100))
	_ = mw.Close()
	expect413(t, postLimit(app, "/bodylimit/default", mw.FormDataContentType(), buf.String(), true), "multipart")
}

func TestBodyLimitPerRoute(t *testing.T) {
	app := newStarted(t)
	big := jsonBody(2 << 20)
	if rec := postLimit(app, "/bodylimit/big", "application/json", big, true); rec.Code != 200 || rec.Body.String() != "2097152\n" {
		t.Fatalf("raised limit: %d %s", rec.Code, rec.Body.String())
	}
	if rec := postLimit(app, "/bodylimit/unlimited", "application/json", big, false); rec.Code != 200 {
		t.Fatalf("MaxBody(0): %d", rec.Code)
	}
	expect413(t, postLimit(app, "/bodylimit/small", "application/json", jsonBody(20), true), "lowered limit")
}

func TestBodyLimitAppOption(t *testing.T) {
	app := newStarted(t, WithMaxBody(32))
	expect413(t, postLimit(app, "/bodylimit/default", "application/json", jsonBody(40), true), "WithMaxBody(32)")
	if rec := postLimit(app, "/bodylimit/default", "application/json", jsonBody(5), true); rec.Code != 200 {
		t.Fatalf("under the limit: %d", rec.Code)
	}
	// A route option wins over the app option.
	if rec := postLimit(app, "/bodylimit/big", "application/json", jsonBody(1000), true); rec.Code != 200 {
		t.Fatalf("route override: %d", rec.Code)
	}

	unlimited := newStarted(t, WithMaxBody(0))
	if rec := postLimit(unlimited, "/bodylimit/default", "application/json", jsonBody(2<<20), true); rec.Code != 200 {
		t.Fatalf("WithMaxBody(0): %d", rec.Code)
	}
}

func TestBodyLimitStreamAndRaw(t *testing.T) {
	app := newStarted(t)
	big := strings.Repeat("x", 2<<20)
	if rec := postLimit(app, "/bodylimit/stream", "application/octet-stream", big, true); rec.Code != 200 || rec.Body.String() != "2097152\n" {
		t.Fatalf("struct{} In must not get the app limit: %d %s", rec.Code, rec.Body.String())
	}
	limitCalls.Store(0)
	expect413(t, postLimit(app, "/bodylimit/stream-capped", "application/octet-stream", big, false), "struct{} with MaxBody")
	if limitCalls.Load() != 0 {
		t.Fatal("handler called for an oversized body")
	}
	if rec := postLimit(app, "/bodylimit/raw", "application/json", big, true); rec.Code != 200 || rec.Body.String() != "2097152" {
		t.Fatalf("raw handler limited: %d %s", rec.Code, rec.Body.String())
	}
}

func TestDefaultReadHeaderTimeout(t *testing.T) {
	var got time.Duration
	ctx, cancel := context.WithCancel(context.Background())
	s := serveApp(t, ctx, &probeLog{}, WithHTTPServer(func(srv *http.Server) {
		got = srv.ReadHeaderTimeout
	}))
	cancel()
	if err := waitResult(t, s.done); err != nil {
		t.Fatal(err)
	}
	if got != DefaultReadHeaderTimeout || got == 0 {
		t.Fatalf("ReadHeaderTimeout = %s, want %s", got, DefaultReadHeaderTimeout)
	}
}
