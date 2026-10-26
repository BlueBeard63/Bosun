package bosun

import (
	"reflect"
	"sync"
)

// --- typed routes ---
//
// Handlers have the shape func(ctx context.Context, req *Req[In]) (Out, error).
// Req[In] embeds *http.Request (so handlers can pull headers, cookies, TLS
// state, etc. directly) and exposes a Body field of type In.
//
// Body parsing depends on In:
//   - struct: JSON body decoded into Body, plus per-field binding via
//     `path:"x"`, `query:"x"`, `header:"X-Foo"`, and `form:"x"` tags. Form
//     bodies (application/x-www-form-urlencoded, multipart/form-data) are
//     parsed via ParseForm instead of JSON decode.
//   - string: raw body assigned to Body verbatim (no parse).
//   - struct{}: body ignored; no parse attempted.
//
// The adapter encodes Out as JSON, maps errors to status codes, emits audit
// events, and records the route for OpenAPI generation.

// RouteInfo describes one mounted route, for OpenAPI generation and tooling.
// Typed routes (bosun.Get(r, ...)) have In and Out set; raw routes
// (r.Get(...)) have Raw true and In/Out nil.
type RouteInfo struct {
	Method   string
	Path     string
	Handler  string
	In       reflect.Type
	Out      reflect.Type
	Status   int   // success status set via bosun.Status(...); 0 means 200
	Declared []int // error statuses declared via bosun.Errors(...)

	// Raw is true for routes mounted with the untyped r.Get/r.Post/...
	// escape hatch. They appear in app.Routes() but not app.TypedRoutes().
	Raw bool

	// Meta holds the metadata attached with bosun.Meta or MWRef.WithMeta on
	// the controller, its groups and the route itself. Nil when none.
	Meta map[string]any

	// Middleware names the controller, group and route middleware wrapping
	// the handler, outermost first: the type for bosun.Use[T]() (e.g.
	// "mw.RequireAuth"), the function name for bosun.UseFunc(f). App-wide
	// middleware (WithMiddleware) is not listed.
	Middleware []string
}

// MetaPermissions is the conventional Meta key for the permissions a route
// requires. Its value is a []string. Like any []string metadata, values set
// at several levels (controller, group, route, middleware helpers)
// accumulate rather than replace each other. Tooling such as OpenAPI
// generation reads it, so permission middleware should attach it:
//
//	func Require(perm string) bosun.MWRef {
//	    return bosun.UseFunc(requirePerm(perm)).
//	        WithMeta(bosun.MetaPermissions, []string{perm})
//	}
const MetaPermissions = "permissions"

// RouteOpt configures a typed route: middleware refs (bosun.Use[T]()),
// metadata (bosun.Meta(...)) and declared error statuses (bosun.Errors(...)).
type RouteOpt interface{ routeOpt() }

func (MWRef) routeOpt() {}

type declaredErrors []int

func (declaredErrors) routeOpt() {}

// Errors declares the error status codes a route can return, for OpenAPI
// generation in binaries where source scanning isn't available or desired.
func Errors(codes ...int) RouteOpt { return declaredErrors(codes) }

// routeTable holds the route metadata one App has mounted. Each App owns its
// own, so apps built in the same process (e.g. in tests) never see each
// other's routes. Observed statuses are kept separately (see observed.go).
type routeTable struct {
	mu     sync.Mutex
	routes []RouteInfo // typed routes only
	all    []RouteInfo // typed and raw routes, in mount order
}

func (t *routeTable) add(ri RouteInfo) {
	t.mu.Lock()
	if !ri.Raw {
		t.routes = append(t.routes, ri)
	}
	t.all = append(t.all, ri)
	t.mu.Unlock()
}

func (t *routeTable) snapshot() []RouteInfo {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]RouteInfo(nil), t.routes...)
}

func (t *routeTable) snapshotAll() []RouteInfo {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]RouteInfo(nil), t.all...)
}

// TypedRoutes returns the typed routes mounted on this app, in mount order.
// Complete after Start; OpenAPI generation and the deploy manifest read it.
// Raw routes are left out; Routes includes them.
func (a *App) TypedRoutes() []RouteInfo { return a.routes.snapshot() }

// Routes returns every route mounted by this app's controllers, typed and
// raw (Raw true), in mount order. Complete after Start. Use it for checks
// that must cover every route, e.g. that each mutating route declares a
// permission:
//
//	for _, rt := range app.Routes() {
//	    if rt.Method != http.MethodGet && rt.Meta[bosun.MetaPermissions] == nil {
//	        t.Errorf("%s %s declares no permission", rt.Method, rt.Path)
//	    }
//	}
func (a *App) Routes() []RouteInfo { return a.routes.snapshotAll() }

// Process-wide aggregate kept for the deprecated package-level TypedRoutes.
var (
	routeIndexMu sync.Mutex
	routeIndex   []RouteInfo
)

func recordRoute(a *App, ri RouteInfo) {
	a.routes.add(ri)
	if ri.Raw {
		return // the deprecated aggregate stays typed-only
	}
	routeIndexMu.Lock()
	routeIndex = append(routeIndex, ri)
	routeIndexMu.Unlock()
}

// TypedRoutes returns every typed route mounted by any App in this process,
// cumulatively — routes from earlier apps (e.g. other tests) are included.
//
// Deprecated: use app.TypedRoutes(), which is scoped to one App. This
// package-level aggregate will be removed in a future release.
func TypedRoutes() []RouteInfo {
	routeIndexMu.Lock()
	defer routeIndexMu.Unlock()
	out := make([]RouteInfo, len(routeIndex))
	copy(out, routeIndex)
	return out
}
