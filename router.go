package bosun

import (
	"fmt"
	"net/http"
	"path"
	"reflect"
	"runtime"
	"strings"
)

// --- router ---

// Router mounts routes for one controller. Sub-groups created via Group
// share the controller's error slice — any registration failure surfaces
// from app.Start() regardless of which group raised it.
type Router struct {
	app    *App
	ctrl   reflect.Type // controller mounting these routes, for error messages
	prefix string
	base   []MiddlewareHandler
	errs   *[]error

	meta    []metaKV // controller/group metadata inherited by every route
	mwNames []string // controller/group middleware names, outermost first
}

// Group returns a child router that mounts under prefix (joined onto the
// parent's prefix) and inherits the parent's middleware stack, appending
// mws on top. The parent and child can both mount routes — child routes
// see the combined prefix and middleware.
//
//	func (c *Admin) Routes(r *bosun.Router) {
//	    bosun.Get(r, "/ping", c.Ping)   // /admin/ping
//
//	    staff := r.Group("/staff", bosun.Use[mw.RequireStaff]())
//	    bosun.Get(staff,  "/users",  c.ListUsers)  // /admin/staff/users + RequireStaff
//	    bosun.Post(staff, "/wipe",   c.Wipe)       // /admin/staff/wipe + RequireStaff
//
//	    api := staff.Group("/v2")              // groups nest freely
//	    bosun.Get(api, "/metrics", c.Metrics)   // /admin/staff/v2/metrics + RequireStaff
//	}
//
// Empty or "/" prefix leaves the parent's prefix unchanged — useful for
// applying middleware to a group of routes without adding a path segment.
func (r *Router) Group(prefix string, mws ...MWRef) *Router {
	sub := &Router{
		app:    r.app,
		ctrl:   r.ctrl,
		prefix: r.prefix,
		base:   append([]MiddlewareHandler{}, r.base...),
		errs:   r.errs,

		meta:    append([]metaKV(nil), r.meta...),
		mwNames: append([]string(nil), r.mwNames...),
	}
	sub.inherit(mws)
	if prefix != "" && prefix != "/" {
		sub.prefix = joinPrefix(sub.prefix, normalizePath(prefix))
	}
	if len(mws) > 0 {
		addl, err := r.app.resolveMWs(mws)
		if err != nil {
			*r.errs = append(*r.errs, err)
			return sub
		}
		sub.base = append(sub.base, addl...)
	}
	return sub
}

// Get mounts a raw net/http handler at p — the untyped escape hatch. Use
// this only when you need full control of the ResponseWriter (streaming,
// SSE, hijack, file downloads). You give up audit events, OpenAPI
// generation, and typed binding. The route is still listed in app.Routes()
// with Raw set, carrying any bosun.Meta passed in mws.
//
// For typed handlers, call bosun.Get(r, p, handler) instead.
func (r *Router) Get(p string, h http.HandlerFunc, mws ...MWRef) { r.raw("GET", p, h, mws) }

// Post mounts a raw net/http POST handler at p. See Get for when to use
// this vs the typed bosun.Post(r, ...).
func (r *Router) Post(p string, h http.HandlerFunc, mws ...MWRef) { r.raw("POST", p, h, mws) }

// Put mounts a raw net/http PUT handler at p. See Get for when to use this
// vs the typed bosun.Put(r, ...).
func (r *Router) Put(p string, h http.HandlerFunc, mws ...MWRef) { r.raw("PUT", p, h, mws) }

// Delete mounts a raw net/http DELETE handler at p. See Get for when to
// use this vs the typed bosun.Delete(r, ...).
func (r *Router) Delete(p string, h http.HandlerFunc, mws ...MWRef) {
	r.raw("DELETE", p, h, mws)
}

// Patch mounts a raw net/http PATCH handler at p. See Get for when to use
// this vs the typed bosun.Patch(r, ...).
func (r *Router) Patch(p string, h http.HandlerFunc, mws ...MWRef) { r.raw("PATCH", p, h, mws) }

// raw mounts an untyped route and records it in app.Routes() with Raw set,
// so route-wide checks see it; typed routes are recorded by typed() instead.
func (r *Router) raw(method, p string, h http.HandlerFunc, refs []MWRef) {
	meta, names := r.describe(refs)
	recordRoute(r.app, RouteInfo{
		Method:     method,
		Path:       r.fullPath(p),
		Handler:    runtime.FuncForPC(reflect.ValueOf(h).Pointer()).Name(),
		Raw:        true,
		Meta:       meta,
		Middleware: names,
	})
	r.handle(method, p, h, refs)
}

func (r *Router) handle(method, p string, h http.HandlerFunc, refs []MWRef) {
	routeMWs, err := r.app.resolveMWs(refs)
	if err != nil {
		*r.errs = append(*r.errs, err)
		return
	}
	var handler http.Handler = h
	all := append(append([]MiddlewareHandler{}, r.base...), routeMWs...)
	for i := len(all) - 1; i >= 0; i-- {
		handler = all[i].Handle(handler)
	}
	r.mount(method+" "+r.fullPath(p), handler)
}

// mount registers pattern on the mux, turning a duplicate or conflicting
// pattern into a Start error instead of the mux's panic.
func (r *Router) mount(pattern string, h http.Handler) {
	if prev, ok := r.app.mounted[pattern]; ok {
		*r.errs = append(*r.errs, fmt.Errorf("bosun: duplicate route %q registered by %v and %v", pattern, prev, r.ctrl))
		return
	}
	defer func() {
		if p := recover(); p != nil {
			*r.errs = append(*r.errs, fmt.Errorf("bosun: route %q on %v: %v", pattern, r.ctrl, p))
		}
	}()
	r.app.Mux.Handle(pattern, h)
	if r.app.mounted == nil {
		r.app.mounted = map[string]reflect.Type{}
	}
	r.app.mounted[pattern] = r.ctrl
}

// fullPath is the mux pattern path for p mounted on this router. It is the
// single source of truth for both the registered route and its RouteInfo,
// so OpenAPI and the manifest advertise exactly the path that is served.
// path.Join drops a trailing slash: "/" under "/notes" serves "/notes".
func (r *Router) fullPath(p string) string {
	full := normalizePath(p)
	if r.prefix != "" {
		full = path.Join(r.prefix, full)
	}
	return full
}

// normalizePath rewrites :name path params to the {name} form Go's
// net/http ServeMux expects. Idempotent — {name} segments are left alone.
//
//	/users/:id/posts/:slug → /users/{id}/posts/{slug}
//	/users/{id}            → /users/{id}
func normalizePath(p string) string {
	if !strings.Contains(p, ":") {
		return p
	}
	parts := strings.Split(p, "/")
	for i, seg := range parts {
		if strings.HasPrefix(seg, ":") && len(seg) > 1 {
			parts[i] = "{" + seg[1:] + "}"
		}
	}
	return strings.Join(parts, "/")
}

// extractParamNames pulls the param names out of a normalized path pattern.
// Expects the {name} form produced by normalizePath. Trailing wildcards
// like {rest...} are reported under the bare name ("rest").
//
//	/users/{id}/posts/{slug} → []string{"id", "slug"}
//	/files/{path...}         → []string{"path"}
func extractParamNames(p string) []string {
	if !strings.Contains(p, "{") {
		return nil
	}
	var names []string
	for _, seg := range strings.Split(p, "/") {
		if !strings.HasPrefix(seg, "{") || !strings.HasSuffix(seg, "}") || len(seg) <= 2 {
			continue
		}
		name := strings.TrimSuffix(seg[1:len(seg)-1], "...")
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}
