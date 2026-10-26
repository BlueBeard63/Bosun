package bosun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"testing"
)

type rawText string
type rawBytes []byte

type respCtrl struct{}

var _ = Controller[respCtrl]("/resp")

func (c *respCtrl) Routes(r *Router) {
	Get(r, "/raw-json", func(context.Context, *Req[struct{}]) (json.RawMessage, error) {
		return json.RawMessage(`{"a":1}`), nil
	})
	Post(r, "/named-string", func(_ context.Context, req *Req[rawText]) (rawText, error) {
		return "echo:" + req.Body, nil
	})
	Get(r, "/named-bytes", func(context.Context, *Req[struct{}]) (rawBytes, error) {
		return rawBytes("bytes"), nil
	})
	Get(r, "/nan", func(context.Context, *Req[struct{}]) (map[string]float64, error) {
		return map[string]float64{"x": math.NaN()}, nil
	})
	Get(r, "/joined", func(context.Context, *Req[struct{}]) (struct{}, error) {
		return struct{}{}, errors.Join(errors.New("other"), E(http.StatusNotFound, "nf", nil))
	})
	Get(r, "/multi-wrap", func(context.Context, *Req[struct{}]) (struct{}, error) {
		return struct{}{}, fmt.Errorf("%w: %w", errors.New("ctx"), E(http.StatusConflict, "dup", nil))
	})
	Get(r, "/zero-status", func(context.Context, *Req[struct{}]) (struct{}, error) {
		return struct{}{}, E(0, "bad", nil)
	})
}

func TestRawMessageOutIsJSON(t *testing.T) {
	rec := do(newStarted(t), "GET", "/resp/raw-json", "")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if got := rec.Body.String(); got != "{\"a\":1}\n" {
		t.Fatalf("body = %q", got)
	}
}

func TestNamedStringAndBytes(t *testing.T) {
	app := newStarted(t)
	rec := do(app, "POST", "/resp/named-string", "hi")
	if rec.Code != 200 || rec.Body.String() != "echo:hi" {
		t.Fatalf("named string: %d %q", rec.Code, rec.Body)
	}
	rec = do(app, "GET", "/resp/named-bytes", "")
	if rec.Code != 200 || rec.Body.String() != "bytes" || rec.Header().Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("named bytes: %d %q %q", rec.Code, rec.Body, rec.Header().Get("Content-Type"))
	}
}

func TestUnencodableOutIs500(t *testing.T) {
	app, aud := newAuditedApp(t)
	rec := do(app, "GET", "/resp/nan", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500: %s", rec.Code, rec.Body)
	}
	ev := aud.last(t)
	if ev.Status != http.StatusInternalServerError || ev.Err == "" {
		t.Fatalf("audit recorded success: %+v", ev)
	}
}

func TestErrorStatusThroughJoinAndInvalid(t *testing.T) {
	app := newStarted(t)
	for path, want := range map[string]int{
		"/resp/joined":      http.StatusNotFound,
		"/resp/multi-wrap":  http.StatusConflict,
		"/resp/zero-status": http.StatusInternalServerError,
	} {
		if rec := do(app, "GET", path, ""); rec.Code != want {
			t.Fatalf("%s: status %d, want %d", path, rec.Code, want)
		}
	}
}
