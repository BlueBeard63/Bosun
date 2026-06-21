package bosun

import (
	"net/http"
	"path"
	"strings"
)

// --- router ---

// Router mounts routes for one controller.
type Router struct {
	app    *App
	prefix string
	base   []MiddlewareHandler
	errs   []error
}

// Get mounts a raw net/http handler at p — the untyped escape hatch. Use
// this only when you need full control of the ResponseWriter (streaming,
// SSE, hijack, file downloads). You give up audit events, OpenAPI
// generation, and typed binding.
//
// For typed handlers, call bosun.Get(r, p, handler) instead.
func (r *Router) Get(p string, h http.HandlerFunc, mws ...MWRef) { r.handle("GET", p, h, mws) }

// Post mounts a raw net/http POST handler at p. See Get for when to use
// this vs the typed bosun.Post(r, ...).
func (r *Router) Post(p string, h http.HandlerFunc, mws ...MWRef) { r.handle("POST", p, h, mws) }

// Put mounts a raw net/http PUT handler at p. See Get for when to use this
// vs the typed bosun.Put(r, ...).
func (r *Router) Put(p string, h http.HandlerFunc, mws ...MWRef) { r.handle("PUT", p, h, mws) }

// Delete mounts a raw net/http DELETE handler at p. See Get for when to
// use this vs the typed bosun.Delete(r, ...).
func (r *Router) Delete(p string, h http.HandlerFunc, mws ...MWRef) {
	r.handle("DELETE", p, h, mws)
}

// Patch mounts a raw net/http PATCH handler at p. See Get for when to use
// this vs the typed bosun.Patch(r, ...).
func (r *Router) Patch(p string, h http.HandlerFunc, mws ...MWRef) { r.handle("PATCH", p, h, mws) }

func (r *Router) handle(method, p string, h http.HandlerFunc, refs []MWRef) {
	routeMWs, err := r.app.resolveMWs(refs)
	if err != nil {
		r.errs = append(r.errs, err)
		return
	}
	var handler http.Handler = h
	all := append(append([]MiddlewareHandler{}, r.base...), routeMWs...)
	for i := len(all) - 1; i >= 0; i-- {
		handler = all[i].Handle(handler)
	}
	full := normalizePath(p)
	if r.prefix != "" {
		full = path.Join(r.prefix, full)
	}
	r.app.Mux.Handle(method+" "+full, handler)
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
