package bosun

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// requirePerm is the kind of helper a permissions module would ship: the
// middleware also records what it enforces.
func requirePerm(perm string) MWRef {
	return UseFunc(func(next http.Handler) http.Handler { return next }).
		WithMeta(MetaPermissions, []string{perm})
}

type metaCtrl struct{}

var _ = Controller[metaCtrl]("/meta", Use[tMW1](), Meta("owner", "notes-team"))

func (c *metaCtrl) Routes(r *Router) {
	Get(r, "/notes", c.list)
	Post(r, "/notes", c.create, requirePerm("notes:write"), Meta("audit", "notes.create"))
	r.Delete("/notes/{id}", c.rawDelete, Meta(MetaPermissions, []string{"notes:delete"}))

	admin := r.Group("/admin", requirePerm("admin"), Meta("owner", "platform"))
	Put(admin, "/settings", c.create, Use[tMW2](), requirePerm("settings:write"))
}

func (c *metaCtrl) list(context.Context, *Req[struct{}]) ([]string, error) { return nil, nil }
func (c *metaCtrl) create(context.Context, *Req[struct{}]) (struct{}, error) {
	return struct{}{}, nil
}
func (c *metaCtrl) rawDelete(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func metaRoutes(rs []RouteInfo) map[string]RouteInfo {
	out := map[string]RouteInfo{}
	for _, r := range rs {
		if strings.HasPrefix(r.Path, "/meta/") {
			out[r.Method+" "+r.Path] = r
		}
	}
	return out
}

func TestRoutesIncludesRawRoutes(t *testing.T) {
	app := newStarted(t)
	all := metaRoutes(app.Routes())
	if len(all) != 4 {
		t.Fatalf("Routes() has %d /meta routes, want 4: %v", len(all), all)
	}
	raw, ok := all["DELETE /meta/notes/{id}"]
	if !ok || !raw.Raw || raw.In != nil || raw.Out != nil {
		t.Fatalf("raw route = %+v, want Raw with nil In/Out", raw)
	}
	if !strings.Contains(raw.Handler, "rawDelete") {
		t.Fatalf("raw handler name %q", raw.Handler)
	}
	if all["POST /meta/notes"].Raw {
		t.Fatal("typed route marked Raw")
	}

	typed := metaRoutes(app.TypedRoutes())
	if len(typed) != 3 {
		t.Fatalf("TypedRoutes() has %d /meta routes, want 3 (raw excluded)", len(typed))
	}
	for _, r := range TypedRoutes() { //nolint:staticcheck // deprecated aggregate
		if r.Raw {
			t.Fatalf("deprecated TypedRoutes() includes raw route %s %s", r.Method, r.Path)
		}
	}

	// The raw route is still served.
	if rec := do(app, "DELETE", "/meta/notes/1", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("raw route status %d", rec.Code)
	}
}

func TestRouteMeta(t *testing.T) {
	all := metaRoutes(newStarted(t).Routes())

	cases := []struct {
		route string
		meta  map[string]any
		mws   []string
	}{
		{"GET /meta/notes", map[string]any{"owner": "notes-team"}, []string{"bosun.tMW1"}},
		{"POST /meta/notes", map[string]any{
			"owner": "notes-team", MetaPermissions: []string{"notes:write"}, "audit": "notes.create",
		}, []string{"bosun.tMW1", "requirePerm"}},
		{"DELETE /meta/notes/{id}", map[string]any{
			"owner": "notes-team", MetaPermissions: []string{"notes:delete"},
		}, []string{"bosun.tMW1"}},
		// Group meta is inherited; []string values accumulate, others are
		// replaced by the innermost level.
		{"PUT /meta/admin/settings", map[string]any{
			"owner": "platform", MetaPermissions: []string{"admin", "settings:write"},
		}, []string{"bosun.tMW1", "requirePerm", "bosun.tMW2", "requirePerm"}},
	}
	for _, tc := range cases {
		ri, ok := all[tc.route]
		if !ok {
			t.Fatalf("%s not listed", tc.route)
		}
		if !reflect.DeepEqual(ri.Meta, tc.meta) {
			t.Errorf("%s Meta = %v, want %v", tc.route, ri.Meta, tc.meta)
		}
		if len(ri.Middleware) != len(tc.mws) {
			t.Errorf("%s Middleware = %v, want %v", tc.route, ri.Middleware, tc.mws)
			continue
		}
		for i, want := range tc.mws {
			if !strings.Contains(ri.Middleware[i], want) {
				t.Errorf("%s Middleware[%d] = %q, want it to contain %q", tc.route, i, ri.Middleware[i], want)
			}
		}
	}
}

func TestRouteMetaNilWhenUnset(t *testing.T) {
	for _, r := range newStarted(t).TypedRoutes() {
		if r.Path == "/appmw/typed" && r.Meta != nil {
			t.Fatalf("route without metadata has Meta %v", r.Meta)
		}
	}
}

// The docs example: every mutating route must declare a permission.
func TestEveryMutatingRouteDeclaresPermission(t *testing.T) {
	for _, r := range metaRoutes(newStarted(t).Routes()) {
		if r.Method == http.MethodGet {
			continue
		}
		if _, ok := r.Meta[MetaPermissions]; !ok {
			t.Errorf("%s %s declares no permission", r.Method, r.Path)
		}
	}
}

// Meta-only refs add no middleware and may be passed anywhere MWRefs are.
func TestMetaIsNotMiddleware(t *testing.T) {
	app := newStarted(t, WithMiddleware(Meta("ignored", true)))
	rec := do(app, "GET", "/meta/notes", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if got := rec.Header().Values("X-Trace"); len(got) != 1 || got[0] != "ctrl-mw" {
		t.Fatalf("X-Trace = %v, want only the controller middleware", got)
	}
}

func TestWithMetaDoesNotAlias(t *testing.T) {
	base := Meta("a", 1)
	x := base.WithMeta("b", 2)
	y := base.WithMeta("c", 3)
	if len(base.meta) != 1 || x.meta[1].key != "b" || y.meta[1].key != "c" {
		t.Fatalf("WithMeta aliased: base=%v x=%v y=%v", base.meta, x.meta, y.meta)
	}
}
