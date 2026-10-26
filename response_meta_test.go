package bosun

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

type noteOut struct {
	ID string `json:"id"`
}

type respMetaCtrl struct{}

var _ = Controller[respMetaCtrl]("/respmeta")

func (c *respMetaCtrl) Routes(r *Router) {
	// Static 201 via the route option, Location via the context.
	Post(r, "/notes", func(ctx context.Context, _ *Req[struct{}]) (noteOut, error) {
		ResponseHeader(ctx).Set("Location", "/respmeta/notes/n1")
		return noteOut{ID: "n1"}, nil
	}, Status(http.StatusCreated))
	// Dynamic status, set through a context derived from the handler's.
	Put(r, "/notes/{id}", func(ctx context.Context, req *Req[struct {
		ID string `path:"id"`
	}]) (noteOut, error) {
		ctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		if req.Body.ID == "new" {
			SetStatus(ctx, http.StatusCreated)
		}
		return noteOut{ID: req.Body.ID}, nil
	})
	// 204 sends no body even though Out has one.
	Delete(r, "/notes/{id}", func(ctx context.Context, _ *Req[struct{}]) (noteOut, error) {
		return noteOut{ID: "gone"}, nil
	}, Status(http.StatusNoContent))
	Post(r, "/accept", func(ctx context.Context, _ *Req[struct{}]) (noteOut, error) {
		SetStatus(ctx, http.StatusAccepted)
		ResponseHeader(ctx).Set("Retry-After", "5")
		return noteOut{ID: "job"}, nil
	}, Status(http.StatusCreated))
	Post(r, "/login", func(ctx context.Context, req *Req[loginIn]) (struct{}, error) {
		if req.Body.Password != "hunter2" {
			ResponseHeader(ctx).Set("WWW-Authenticate", `Bearer realm="bosun"`)
			return struct{}{}, E(http.StatusUnauthorized, "invalid credentials", nil)
		}
		SetCookie(ctx, &http.Cookie{Name: "session", Value: "s-1", Path: "/", HttpOnly: true})
		return struct{}{}, nil
	})
	Get(r, "/page", func(ctx context.Context, _ *Req[struct{}]) (string, error) {
		ResponseHeader(ctx).Set("Content-Type", "text/html; charset=utf-8")
		return "<p>hi</p>", nil
	})
	Get(r, "/fail", func(ctx context.Context, _ *Req[struct{}]) (string, error) {
		ResponseHeader(ctx).Set("Content-Type", "text/html; charset=utf-8")
		SetStatus(ctx, http.StatusCreated)
		return "", errors.New("boom")
	})
}

func TestTypedStatusCreatedWithLocation(t *testing.T) {
	app, aud := newAuditedApp(t)
	rec := do(app, "POST", "/respmeta/notes", "")
	if rec.Code != http.StatusCreated || rec.Header().Get("Location") != "/respmeta/notes/n1" {
		t.Fatalf("got %d Location=%q", rec.Code, rec.Header().Get("Location"))
	}
	if strings.TrimSpace(rec.Body.String()) != `{"id":"n1"}` || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("body %q type %q", rec.Body.String(), rec.Header().Get("Content-Type"))
	}
	if ev := aud.last(t); ev.Status != http.StatusCreated {
		t.Fatalf("audit status %d", ev.Status)
	}
	if got := app.ObservedStatuses("POST", "/respmeta/notes"); len(got) != 1 || got[0] != 201 {
		t.Fatalf("observed %v", got)
	}

	if rec := do(app, "PUT", "/respmeta/notes/new", ""); rec.Code != http.StatusCreated {
		t.Fatalf("SetStatus via derived ctx: %d", rec.Code)
	}
	if rec := do(app, "PUT", "/respmeta/notes/n1", ""); rec.Code != http.StatusOK {
		t.Fatalf("default status: %d", rec.Code)
	}
}

func TestTypedStatusNoContent(t *testing.T) {
	rec := do(newStarted(t), "DELETE", "/respmeta/notes/n1", "")
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != "" {
		t.Fatalf("got %d body=%q type=%q", rec.Code, rec.Body.String(), rec.Header().Get("Content-Type"))
	}
}

func TestSetStatusOverridesRouteStatus(t *testing.T) {
	rec := do(newStarted(t), "POST", "/respmeta/accept", "")
	if rec.Code != http.StatusAccepted || rec.Header().Get("Retry-After") != "5" {
		t.Fatalf("got %d Retry-After=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
}

func TestTypedSetCookieAndErrorHeaders(t *testing.T) {
	app := newStarted(t)
	rec := do(app, "POST", "/respmeta/login", `{"email":"a","password":"hunter2"}`)
	cookies := rec.Result().Cookies()
	if rec.Code != 200 || len(cookies) != 1 || cookies[0].Name != "session" || cookies[0].Value != "s-1" || !cookies[0].HttpOnly {
		t.Fatalf("got %d cookies %v", rec.Code, cookies)
	}

	rec = do(app, "POST", "/respmeta/login", `{"email":"a","password":"nope"}`)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != `Bearer realm="bosun"` {
		t.Fatalf("got %d WWW-Authenticate=%q", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
}

func TestResponseHeaderContentType(t *testing.T) {
	app := newStarted(t)
	rec := do(app, "GET", "/respmeta/page", "")
	if rec.Header().Get("Content-Type") != "text/html; charset=utf-8" || rec.Body.String() != "<p>hi</p>" {
		t.Fatalf("type %q body %q", rec.Header().Get("Content-Type"), rec.Body.String())
	}
	// An error response is JSON with the error status, whatever was set.
	rec = do(app, "GET", "/respmeta/fail", "")
	if rec.Code != 500 || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("error: %d type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestResponseHelpersOutsideTypedHandler(t *testing.T) {
	ctx := context.Background()
	SetStatus(ctx, http.StatusCreated) // no-op
	SetCookie(ctx, &http.Cookie{Name: "x"})
	if h := ResponseHeader(ctx); h == nil {
		t.Fatal("ResponseHeader must not return nil")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("SetStatus(404) should panic")
		}
	}()
	SetStatus(ctx, http.StatusNotFound)
}

func TestStatusRouteOption(t *testing.T) {
	for _, rt := range newStarted(t).TypedRoutes() {
		if rt.Method == "POST" && rt.Path == "/respmeta/notes" && rt.Status != 201 {
			t.Fatalf("RouteInfo.Status = %d", rt.Status)
		}
		if rt.Method == "PUT" && rt.Path == "/respmeta/notes/{id}" && rt.Status != 0 {
			t.Fatalf("RouteInfo.Status = %d without bosun.Status", rt.Status)
		}
	}

	app := newStarted(t)
	var errs []error
	r := &Router{app: app, prefix: "/badstatus", errs: &errs}
	Get(r, "/x", func(context.Context, *Req[struct{}]) (struct{}, error) { return struct{}{}, nil }, Status(404))
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "bosun.Status(404): not a 2xx or 3xx status") {
		t.Fatalf("errs = %v", errs)
	}
}
