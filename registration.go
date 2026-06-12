package bosun

import (
	"fmt"
	"reflect"

	"github.com/amberstack/bosun/registry"
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
	if _, ok := probe.(HasRoutes); !ok {
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

// MWRef is a type-safe reference to registered middleware.
type MWRef struct{ t reflect.Type }

// Use references middleware type T: bosun.Use[LoggingMiddleware]().
func Use[T any]() MWRef {
	return MWRef{t: reflect.TypeOf((*T)(nil))}
}
