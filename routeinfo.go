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

var (
	routeIndexMu sync.Mutex
	routeIndex   []RouteInfo
)

// TypedRoutes returns every typed route registered so far. Complete after
// App.Start; OpenAPI generators read this.
func TypedRoutes() []RouteInfo {
	routeIndexMu.Lock()
	defer routeIndexMu.Unlock()
	out := make([]RouteInfo, len(routeIndex))
	copy(out, routeIndex)
	return out
}
