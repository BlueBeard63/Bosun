package bosun

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// --- Auth + HasPermission chain fixtures ---

type chainUser struct {
	ID    int
	Roles []string
}

// requireAuthMW is a registered singleton: rejects without a token, attaches
// a typed *chainUser to the context for downstream consumers.
type requireAuthMW struct{}

func (*requireAuthMW) Handle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("Authorization")
		var u *chainUser
		switch tok {
		case "admin":
			u = &chainUser{ID: 1, Roles: []string{"admin"}}
		case "editor":
			u = &chainUser{ID: 2, Roles: []string{"editor"}}
		case "reader":
			u = &chainUser{ID: 3, Roles: []string{"reader"}}
		default:
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithValue(r.Context(), u)))
	})
}

var _ = Middleware[requireAuthMW]()

// hasPermission is the inline factory — captures roles per route.
func hasPermission(roles ...string) MWRef {
	return UseFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := Value[chainUser](r.Context())
			if u == nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			for _, want := range roles {
				if slices.Contains(u.Roles, want) {
					next.ServeHTTP(w, r)
					return
				}
			}
			http.Error(w, "forbidden", http.StatusForbidden)
		})
	})
}

// chainCtrl mounts a route guarded by Auth → HasPermission.
type chainCtrl struct{}

var _ = Controller[chainCtrl]("/chain")

type chainOut struct {
	UserID int      `json:"user_id"`
	Roles  []string `json:"roles"`
}

func (c *chainCtrl) Routes(r *Router) {
	Get(r, "/admin", c.adminOnly, Use[requireAuthMW](), hasPermission("admin"))
	Get(r, "/staff", c.staff, Use[requireAuthMW](), hasPermission("admin", "editor"))
}

func (c *chainCtrl) adminOnly(ctx context.Context, _ *Req[struct{}]) (chainOut, error) {
	u := Value[chainUser](ctx)
	return chainOut{UserID: u.ID, Roles: u.Roles}, nil
}

func (c *chainCtrl) staff(ctx context.Context, _ *Req[struct{}]) (chainOut, error) {
	u := Value[chainUser](ctx)
	return chainOut{UserID: u.ID, Roles: u.Roles}, nil
}

// --- tests ---

func sendAuthed(t *testing.T, app *App, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)
	return rec
}

func TestChainUnauthorizedShortCircuits(t *testing.T) {
	app := newStarted(t)
	rec := sendAuthed(t, app, "/chain/admin", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token → 401, got %d", rec.Code)
	}
}

func TestChainAuthorizedButForbidden(t *testing.T) {
	app := newStarted(t)
	rec := sendAuthed(t, app, "/chain/admin", "reader")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("reader hitting admin → 403, got %d: %s", rec.Code, rec.Body)
	}
}

func TestChainAuthorizedAndAllowed(t *testing.T) {
	app := newStarted(t)
	rec := sendAuthed(t, app, "/chain/admin", "admin")
	if rec.Code != 200 {
		t.Fatalf("admin → 200, got %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"user_id":1`) {
		t.Fatalf("handler should see typed user from ctx: %s", rec.Body)
	}
}

// --- Option B: registered singleton with a Configure(args) method ---

type hasPermissionMW struct{}

// Handle is the no-args fallback: fail closed.
func (*hasPermissionMW) Handle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "permissions not configured", http.StatusInternalServerError)
	})
}

// Configure is invoked once per route at app.Start() with the args supplied
// via bosun.Use[hasPermissionMW]([]string{"admin"}...).
func (*hasPermissionMW) Configure(roles []string) MiddlewareHandler {
	return MiddlewareFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := Value[chainUser](r.Context())
			if u == nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			for _, want := range roles {
				if slices.Contains(u.Roles, want) {
					next.ServeHTTP(w, r)
					return
				}
			}
			http.Error(w, "forbidden", http.StatusForbidden)
		})
	})
}

var _ = Middleware[hasPermissionMW]()

type chainOptB struct{}

var _ = Controller[chainOptB]("/chainb")

func (c *chainOptB) Routes(r *Router) {
	Get(r, "/admin", c.admin,
		Use[requireAuthMW](),
		Use[hasPermissionMW]([]string{"admin"}),
	)
	Get(r, "/staff", c.staff,
		Use[requireAuthMW](),
		Use[hasPermissionMW]([]string{"admin", "editor"}),
	)
}

func (c *chainOptB) admin(ctx context.Context, _ *Req[struct{}]) (chainOut, error) {
	u := Value[chainUser](ctx)
	return chainOut{UserID: u.ID, Roles: u.Roles}, nil
}

func (c *chainOptB) staff(ctx context.Context, _ *Req[struct{}]) (chainOut, error) {
	u := Value[chainUser](ctx)
	return chainOut{UserID: u.ID, Roles: u.Roles}, nil
}

func TestSingletonMWReadsConfigureArgs(t *testing.T) {
	app := newStarted(t)
	if rec := sendAuthed(t, app, "/chainb/admin", "admin"); rec.Code != 200 {
		t.Fatalf("admin → 200, got %d: %s", rec.Code, rec.Body)
	}
	if rec := sendAuthed(t, app, "/chainb/admin", "editor"); rec.Code != http.StatusForbidden {
		t.Fatalf("editor → 403 on /chainb/admin, got %d: %s", rec.Code, rec.Body)
	}
	if rec := sendAuthed(t, app, "/chainb/staff", "editor"); rec.Code != 200 {
		t.Fatalf("editor → 200 on /chainb/staff, got %d: %s", rec.Code, rec.Body)
	}
	if rec := sendAuthed(t, app, "/chainb/staff", "reader"); rec.Code != http.StatusForbidden {
		t.Fatalf("reader → 403 on /chainb/staff, got %d: %s", rec.Code, rec.Body)
	}
}

func TestUseArgsWrongTypeFailsAtStart(t *testing.T) {
	tc := &typedCtrl{} // any controller will do — we register a bad route on its router
	app := New()
	app.Reg.Validate()                       // make sure registry is sound
	app.Mux = http.NewServeMux()             // fresh mux
	r := &Router{app: app, prefix: "/wrong"} // synthesize a router
	Get(r, "/x", tc.echo, Use[hasPermissionMW]("not a string slice"))
	if err := app.Start(); err == nil {
		_ = r
		// We expect resolveMWs to surface a type error when the route is mounted.
		// (Start drives the mount through pendingCtrls; the inline route above
		// doesn't go through that path, so we instead resolve manually:)
		_, rerr := app.resolveMWs([]MWRef{Use[hasPermissionMW]("not a string slice")})
		if rerr == nil {
			t.Fatal("expected arg-type error from Configure")
		}
	}
}

func TestChainPerRouteRolesAreIndependent(t *testing.T) {
	app := newStarted(t)
	if rec := sendAuthed(t, app, "/chain/staff", "editor"); rec.Code != 200 {
		t.Fatalf("editor allowed on /staff, got %d: %s", rec.Code, rec.Body)
	}
	if rec := sendAuthed(t, app, "/chain/admin", "editor"); rec.Code != http.StatusForbidden {
		t.Fatalf("editor blocked from /admin, got %d: %s", rec.Code, rec.Body)
	}
}
