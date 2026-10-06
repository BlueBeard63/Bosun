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

// RouteInfo describes one typed route, for OpenAPI generation and tooling.
type RouteInfo struct {
	Method   string
	Path     string
	Handler  string
	In       reflect.Type
	Out      reflect.Type
	Declared []int // error statuses declared via bosun.Errors(...)
}

// RouteOpt configures a typed route: middleware refs (bosun.Use[T]()) and
// declared error statuses (bosun.Errors(...)).
type RouteOpt interface{ routeOpt() }

func (MWRef) routeOpt() {}

type declaredErrors []int

func (declaredErrors) routeOpt() {}

// Errors declares the error status codes a route can return, for OpenAPI
// generation in binaries where source scanning isn't available or desired.
func Errors(codes ...int) RouteOpt { return declaredErrors(codes) }

// routeTable holds the typed-route metadata one App has mounted and the
// statuses those routes have returned. Each App owns its own, so apps built
// in the same process (e.g. in tests) never see each other's routes.
type routeTable struct {
	mu       sync.Mutex
	routes   []RouteInfo
	observed map[string]map[int]struct{}
}

func (t *routeTable) add(ri RouteInfo) {
	t.mu.Lock()
	t.routes = append(t.routes, ri)
	t.mu.Unlock()
}

func (t *routeTable) snapshot() []RouteInfo {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]RouteInfo(nil), t.routes...)
}

// TypedRoutes returns the typed routes mounted on this app, in mount order.
// Complete after Start; OpenAPI generation and the deploy manifest read it.
func (a *App) TypedRoutes() []RouteInfo { return a.routes.snapshot() }

// Process-wide aggregate kept for the deprecated package-level TypedRoutes.
var (
	routeIndexMu sync.Mutex
	routeIndex   []RouteInfo
)

func recordRoute(a *App, ri RouteInfo) {
	a.routes.add(ri)
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
