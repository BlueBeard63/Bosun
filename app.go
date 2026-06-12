package bosun

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"

	"github.com/amberstack/bosun/registry"
)

// --- app ---

// App owns the registry and the HTTP mux.
type App struct {
	Reg      *registry.Registry
	Mux      *http.ServeMux
	disabled map[string]bool
	prefixes map[reflect.Type]string
}

// New creates an App, applies options, then applies every enabled
// Service/Middleware/Controller declaration from all imported packages.
// Register external instances (e.g. *gorm.DB) and extension-point
// implementations on app.Reg before calling Start or Run.
func New(opts ...Option) *App {
	app := &App{
		Reg:      registry.New(),
		Mux:      http.NewServeMux(),
		disabled: map[string]bool{},
		prefixes: map[reflect.Type]string{},
	}
	for _, o := range opts {
		o(app)
	}
	registry.RegisterInstance[*registry.Registry](app.Reg, app.Reg)
	for _, s := range pendingServices {
		if app.disabled[s.pkg] {
			continue
		}
		s.apply(app.Reg)
	}
	return app
}

// Start applies module defaults (host registrations win), validates the full
// dependency graph, and mounts all controller routes.
func (a *App) Start() error {
	for _, d := range pendingDefaults {
		if a.disabled[d.pkg] {
			continue
		}
		d.apply(a)
	}
	if err := a.Reg.Validate(); err != nil {
		return err
	}
	for _, c := range pendingCtrls {
		if a.disabled[c.pkg] {
			continue
		}
		inst, err := a.Reg.ResolveType(c.typ)
		if err != nil {
			return err
		}
		base, err := a.resolveMWs(c.mws)
		if err != nil {
			return err
		}
		prefix := c.prefix
		if p, ok := a.prefixes[c.typ]; ok {
			prefix = p
		}
		router := &Router{app: a, prefix: prefix, base: base}
		inst.(HasRoutes).Routes(router)
		if err := errors.Join(router.errs...); err != nil {
			return err
		}
	}
	return nil
}

// Run is Start + ListenAndServe.
func (a *App) Run(addr string) error {
	if err := a.Start(); err != nil {
		return err
	}
	return http.ListenAndServe(addr, a.Mux)
}

// Shutdown closes services (io.Closer) in reverse dependency order.
func (a *App) Shutdown() error { return a.Reg.Shutdown() }

func (a *App) resolveMWs(refs []MWRef) ([]MiddlewareHandler, error) {
	out := make([]MiddlewareHandler, 0, len(refs))
	for _, ref := range refs {
		inst, err := a.Reg.ResolveType(ref.t)
		if err != nil {
			return nil, fmt.Errorf("resolving middleware %v: %w", ref.t, err)
		}
		m, ok := inst.(MiddlewareHandler)
		if !ok {
			return nil, fmt.Errorf("%v does not implement Handle(http.Handler) http.Handler", ref.t)
		}
		out = append(out, m)
	}
	return out, nil
}
