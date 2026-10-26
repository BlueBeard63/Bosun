package bosun

import (
	"fmt"
	"reflect"
	"runtime"
)

// --- route metadata ---

// metaKV is one key/value attached to a MWRef. A slice of these (rather
// than a map) keeps refs cheap to copy and the merge order explicit.
type metaKV struct {
	key string
	val any
}

// Meta attaches a metadata key/value to a route. It is read back from
// RouteInfo.Meta (app.Routes(), app.TypedRoutes()) after Start and has no
// effect on request handling. Meta is accepted wherever middleware is: as a
// typed-route option, on raw routes, on Group and on Controller (where it
// applies to every route below):
//
//	bosun.Post(r, "/notes", c.Create, bosun.Meta("audit", "notes.create"))
//	r.Get("/export", c.Export, bosun.Meta(bosun.MetaPermissions, []string{"notes:export"}))
//	admin := r.Group("/admin", bosun.Meta(bosun.MetaPermissions, []string{"admin"}))
//
// When several levels set the same key, []string values accumulate
// (controller first) and any other value is replaced by the innermost one.
// See MetaPermissions for the conventional permissions key.
func Meta(key string, val any) MWRef {
	return MWRef{meta: []metaKV{{key, val}}}
}

// WithMeta returns a copy of m that also attaches key/val to every route it
// is applied to, like bosun.Meta. Middleware helpers use it so declaring the
// middleware also records what it enforces:
//
//	func Require(perm string) bosun.MWRef {
//	    return bosun.Use[RequirePerm](perm).WithMeta(bosun.MetaPermissions, []string{perm})
//	}
func (m MWRef) WithMeta(key string, val any) MWRef {
	m.meta = append(append([]metaKV(nil), m.meta...), metaKV{key, val})
	return m
}

// metaOnly reports whether m carries metadata but no middleware.
func (m MWRef) metaOnly() bool { return m.t == nil && m.inline == nil && len(m.meta) > 0 }

// name is the middleware name listed in RouteInfo.Middleware, or "" for a
// metadata-only ref.
func (m MWRef) name() string {
	switch {
	case m.t != nil:
		if m.t.Kind() == reflect.Pointer {
			return m.t.Elem().String()
		}
		return m.t.String()
	case m.inline != nil:
		if f, ok := m.inline.(MiddlewareFunc); ok && f != nil {
			if fn := runtime.FuncForPC(reflect.ValueOf(f).Pointer()); fn != nil {
				return fn.Name()
			}
		}
		return fmt.Sprintf("%T", m.inline)
	}
	return ""
}

// inherit records the metadata and middleware names of refs applied at
// controller or group level, so every route mounted below carries them.
func (r *Router) inherit(refs []MWRef) {
	for _, ref := range refs {
		r.meta = append(r.meta, ref.meta...)
		if n := ref.name(); n != "" {
			r.mwNames = append(r.mwNames, n)
		}
	}
}

// describe returns the Meta map and middleware names for a route mounted on
// r with refs. Meta is nil when nothing was attached.
func (r *Router) describe(refs []MWRef) (map[string]any, []string) {
	names := append([]string(nil), r.mwNames...)
	var meta map[string]any
	add := func(kvs []metaKV) {
		for _, kv := range kvs {
			if meta == nil {
				meta = map[string]any{}
			}
			if prev, ok := meta[kv.key].([]string); ok {
				if more, ok := kv.val.([]string); ok {
					meta[kv.key] = append(append([]string(nil), prev...), more...)
					continue
				}
			}
			meta[kv.key] = kv.val
		}
	}
	add(r.meta)
	for _, ref := range refs {
		add(ref.meta)
		if n := ref.name(); n != "" {
			names = append(names, n)
		}
	}
	return meta, names
}
