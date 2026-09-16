package bosun

import (
	"fmt"
	"net/http"
	"reflect"

	"github.com/bluebeard63/bosun/registry"
)

// --- self-registration ---

type svcRec struct {
	pkg   string
	apply func(*registry.Registry)
}

type ctrlRec struct {
	pkg    string
	typ    reflect.Type
	prefix string
	mws    []MWRef
}

type defaultRec struct {
	pkg   string
	apply func(*App)
}

var (
	pendingServices []svcRec
	pendingCtrls    []ctrlRec
	pendingDefaults []defaultRec
)

// Service registers T as an injectable singleton. Fields of T whose types are
// registered are injected automatically; no constructor needed.
func Service[T any]() struct{} {
	t := reflect.TypeOf((*T)(nil))
	pendingServices = append(pendingServices, svcRec{
		pkg: t.Elem().PkgPath(),
		apply: func(reg *registry.Registry) {
			reg.RegisterType(t, func(r *registry.Registry) (any, error) {
				return buildInjected(r, t)
			})
		},
	})
	return struct{}{}
}

// Middleware registers T as injectable middleware implementing
// Handle(next http.Handler) http.Handler. Middleware may have injected fields.
func Middleware[T any]() struct{} {
	var probe any = (*T)(nil)
	if _, ok := probe.(MiddlewareHandler); !ok {
		panic(fmt.Sprintf("bosun: %T must implement Handle(http.Handler) http.Handler", probe))
	}
	return Service[T]()
}

// Controller registers T as a controller mounted at prefix (overridable by
// the host via OverridePrefix). T must implement Routes(*bosun.Router).
func Controller[T any](prefix string, mws ...MWRef) struct{} {
	var probe any = (*T)(nil)
	if _, ok := probe.(BaseController); !ok {
		panic(fmt.Sprintf("bosun: %T must implement Routes(*bosun.Router)", probe))
	}
	_ = Service[T]()
	t := reflect.TypeOf((*T)(nil))
	pendingCtrls = append(pendingCtrls, ctrlRec{
		pkg:    t.Elem().PkgPath(),
		typ:    t,
		prefix: prefix,
		mws:    mws,
	})
	return struct{}{}
}

// Default registers a fallback provider for T, used only if the host hasn't
// registered T itself by the time the app starts. Modules use this to ship
// overridable configuration:
//
//	var _ = bosun.Default[*Options](func() *Options { return &Options{Greeting: "hi"} })
func Default[T any](build func() T) struct{} {
	t := reflect.TypeOf((*T)(nil)).Elem()
	pkg := t.PkgPath()
	if t.Kind() == reflect.Pointer {
		pkg = t.Elem().PkgPath()
	}
	pendingDefaults = append(pendingDefaults, defaultRec{
		pkg: pkg,
		apply: func(a *App) {
			if a.Reg.Has(t) {
				return // host provided its own
			}
			a.Reg.RegisterType(t, func(*registry.Registry) (any, error) {
				return build(), nil
			})
		},
	})
	return struct{}{}
}

// DefaultBind binds interface I to implementation Impl unless the host has
// bound I itself. Impl must also be registered (e.g. via Service[Impl]()).
// Modules use this to ship a default implementation of an extension point
// that hosts can replace.
func DefaultBind[I any, Impl any]() struct{} {
	iface := reflect.TypeOf((*I)(nil)).Elem()
	impl := reflect.TypeOf((*Impl)(nil))
	pendingDefaults = append(pendingDefaults, defaultRec{
		pkg: impl.Elem().PkgPath(),
		apply: func(a *App) {
			if a.Reg.Has(iface) {
				return
			}
			a.Reg.RegisterType(iface, func(r *registry.Registry) (any, error) {
				return r.ResolveType(impl)
			})
		},
	})
	return struct{}{}
}

// MWRef references middleware: either a registered singleton (via Use[T]())
// or an inline factory (via UseFunc / user-defined helpers like
// HasPermission("admin")).
type MWRef struct {
	t      reflect.Type      // set by Use[T]()
	inline MiddlewareHandler // set by UseFunc() and factory helpers
	args   []any             // populated when Use is called with arguments
}

// Use references middleware type T.
//
// With no arguments, T's Handle(next http.Handler) http.Handler is used —
// every route shares the same singleton instance:
//
//	bosun.Use[LoggingMiddleware]()
//
// With arguments, T must define a Configure(...) MiddlewareHandler method
// whose parameter types match the supplied arguments. At app start, the
// framework resolves the singleton, calls Configure(args...), and uses the
// returned MiddlewareHandler for that route. Each call site captures its
// own arguments:
//
//	type HasPermissionMiddleware struct{}
//	func (m *HasPermissionMiddleware) Configure(roles []string) bosun.MiddlewareHandler { ... }
//	func (m *HasPermissionMiddleware) Handle(next http.Handler) http.Handler           { ... }
//	var _ = bosun.Middleware[HasPermissionMiddleware]()
//
//	bosun.Get(r, "/admin", c.Admin,
//	    bosun.Use[RequireAuth](),
//	    bosun.Use[HasPermissionMiddleware]([]string{"admin"}),
//	)
//
// Argument-type mismatches are caught at app.Start(), not at request time.
func Use[T any](args ...any) MWRef {
	return MWRef{t: reflect.TypeOf((*T)(nil)), args: args}
}

// MiddlewareFunc adapts a plain function to MiddlewareHandler.
type MiddlewareFunc func(next http.Handler) http.Handler

// Handle satisfies MiddlewareHandler.
func (f MiddlewareFunc) Handle(next http.Handler) http.Handler { return f(next) }

// UseFunc wraps an inline middleware closure as a MWRef. Use this to write
// factory helpers that take parameters at the route declaration site:
//
//	func HasPermission(roles ...string) bosun.MWRef {
//	    return bosun.UseFunc(func(next http.Handler) http.Handler {
//	        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
//	            u := bosun.Value[AuthUser](r.Context())
//	            if u == nil || !hasAny(u.Roles, roles) {
//	                http.Error(w, "forbidden", http.StatusForbidden)
//	                return
//	            }
//	            next.ServeHTTP(w, r)
//	        })
//	    })
//	}
//
//	bosun.Get(r, "/admin", c.Admin,
//	    bosun.Use[RequireAuth](),
//	    HasPermission("admin", "editor"),
//	)
//
// Each call constructs a fresh closure — there is no shared singleton, so
// the captured parameters are per-route.
func UseFunc(f MiddlewareFunc) MWRef {
	return MWRef{inline: f}
}
