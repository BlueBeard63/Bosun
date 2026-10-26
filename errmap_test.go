package bosun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

var (
	errCardDeclined = errors.New("billing: card declined by gateway 7f3a")
	errUnmapped     = errors.New("db: secret connection string")
)

// goneErr carries its own status, like repomod.ErrNotFound, but no public
// message.
type goneErr struct{}

func (goneErr) Error() string   { return "archive: row 42 purged" }
func (goneErr) HTTPStatus() int { return http.StatusGone }

type teapotErr struct{}

func (teapotErr) Error() string         { return "kettle: internal detail" }
func (teapotErr) HTTPStatus() int       { return http.StatusTeapot }
func (teapotErr) PublicMessage() string { return "short and stout" }

type errMapCtrl struct{}

var _ = Controller[errMapCtrl]("/errmap")

func (c *errMapCtrl) Routes(r *Router) {
	Get(r, "/{kind}", c.fail)
	Post(r, "/validate", c.validate)
}

func (c *errMapCtrl) fail(_ context.Context, req *Req[struct{}]) (struct{}, error) {
	switch req.Params["kind"] {
	case "mapped":
		return struct{}{}, fmt.Errorf("charging order 9: %w", errCardDeclined)
	case "explicit":
		return struct{}{}, E(http.StatusConflict, "explicit wins", errCardDeclined)
	case "gone":
		return struct{}{}, fmt.Errorf("load: %w", goneErr{})
	case "teapot":
		return struct{}{}, teapotErr{}
	}
	return struct{}{}, errUnmapped
}

type validateIn struct {
	Kind string `json:"kind"`
}

func (v validateIn) Validate() error {
	switch v.Kind {
	case "mapped":
		return errCardDeclined
	case "explicit":
		return E(http.StatusUnprocessableEntity, "bad kind", nil)
	}
	return errUnmapped
}

func (c *errMapCtrl) validate(context.Context, *Req[validateIn]) (struct{}, error) {
	return struct{}{}, nil
}

func errMsg(t *testing.T, body string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}
	s, _ := m["error"].(string)
	return s
}

func TestMapError(t *testing.T) {
	app := newStarted(t, MapError(errCardDeclined, http.StatusPaymentRequired, "card declined"))
	plain := newStarted(t)

	cases := []struct {
		app    *App
		target string
		status int
		msg    string
	}{
		{app, "/errmap/mapped", http.StatusPaymentRequired, "card declined"}, // wrapped sentinel
		{plain, "/errmap/mapped", http.StatusInternalServerError, "internal server error"},
		{app, "/errmap/explicit", http.StatusConflict, "explicit wins"}, // *Error beats a mapping
		{app, "/errmap/gone", http.StatusGone, "gone"},                  // own HTTPStatus, status text
		{app, "/errmap/teapot", http.StatusTeapot, "short and stout"},   // own PublicMessage
		{app, "/errmap/other", http.StatusInternalServerError, "internal server error"},
	}
	for _, tc := range cases {
		rec := do(tc.app, "GET", tc.target, "")
		if rec.Code != tc.status || errMsg(t, rec.Body.String()) != tc.msg {
			t.Errorf("%s = %d %s, want %d %q", tc.target, rec.Code, rec.Body, tc.status, tc.msg)
		}
	}
}

func TestMapErrorOverridesOwnStatus(t *testing.T) {
	app := newStarted(t, MapError(goneErr{}, http.StatusNotFound, ""))
	rec := do(app, "GET", "/errmap/gone", "")
	if rec.Code != http.StatusNotFound || errMsg(t, rec.Body.String()) != "not found" {
		t.Fatalf("got %d %s, want 404 not found", rec.Code, rec.Body)
	}
}

func TestMapErrorPanicsOnBadInput(t *testing.T) {
	for _, tc := range []struct {
		target error
		status int
	}{{nil, 404}, {errUnmapped, 0}, {errUnmapped, 1000}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("MapError(%v, %d) did not panic", tc.target, tc.status)
				}
			}()
			MapError(tc.target, tc.status, "x")
		}()
	}
}

func TestValidateErrorsAreNotEchoed(t *testing.T) {
	app := newStarted(t, MapError(errCardDeclined, http.StatusPaymentRequired, "card declined"))
	cases := []struct {
		kind   string
		status int
		msg    string
	}{
		{"plain", http.StatusBadRequest, "invalid request"},
		{"explicit", http.StatusUnprocessableEntity, "bad kind"},
		{"mapped", http.StatusPaymentRequired, "card declined"},
	}
	for _, tc := range cases {
		rec := do(app, "POST", "/errmap/validate", `{"kind":"`+tc.kind+`"}`)
		if rec.Code != tc.status || errMsg(t, rec.Body.String()) != tc.msg {
			t.Errorf("%s: got %d %s, want %d %q", tc.kind, rec.Code, rec.Body, tc.status, tc.msg)
		}
		if strings.Contains(rec.Body.String(), "secret") {
			t.Errorf("%s: validation error leaked: %s", tc.kind, rec.Body)
		}
	}
}

// The hidden cause still reaches the audit event.
func TestValidateErrorReachesAudit(t *testing.T) {
	app, aud := newAuditedApp(t)
	do(app, "POST", "/errmap/validate", `{"kind":"plain"}`)
	if ev := aud.last(t); ev.Err != errUnmapped.Error() {
		t.Fatalf("audit Err = %q, want the Validate error", ev.Err)
	}
}
