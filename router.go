package bosun

import (
	"net/http"
	"path"
)

// --- router ---

// Router mounts routes for one controller.
type Router struct {
	app    *App
	prefix string
	base   []MiddlewareHandler
	errs   []error
}

func (r *Router) Get(p string, h http.HandlerFunc, mws ...MWRef)    { r.handle("GET", p, h, mws) }
func (r *Router) Post(p string, h http.HandlerFunc, mws ...MWRef)   { r.handle("POST", p, h, mws) }
func (r *Router) Put(p string, h http.HandlerFunc, mws ...MWRef)    { r.handle("PUT", p, h, mws) }
func (r *Router) Delete(p string, h http.HandlerFunc, mws ...MWRef) { r.handle("DELETE", p, h, mws) }
func (r *Router) Patch(p string, h http.HandlerFunc, mws ...MWRef)  { r.handle("PATCH", p, h, mws) }

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
	full := p
	if r.prefix != "" {
		full = path.Join(r.prefix, p)
	}
	r.app.Mux.Handle(method+" "+full, handler)
}
