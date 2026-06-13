package bosun

import (
	"context"
	"net/http"
)

// --- typed context values ---
//
// Middleware uses WithValue to attach a *T to the request context; downstream
// handlers retrieve it via Value[T]. The key is the type T itself, so each
// distinct T occupies its own slot — no string keys, no collisions, no
// untyped lookups.

type ctxKey[T any] struct{}

// WithValue returns a copy of ctx that carries v keyed by the type T. Use
// this from middleware to make a *T (auth user, request ID, tenant, etc.)
// available to downstream handlers.
//
//	ctx := bosun.WithValue(r.Context(), user)   // user is *AuthUser
//	next.ServeHTTP(w, r.WithContext(ctx))
func WithValue[T any](ctx context.Context, v *T) context.Context {
	return context.WithValue(ctx, ctxKey[T]{}, v)
}

// Value retrieves the *T previously attached to ctx via WithValue[T], or
// nil if no value was attached.
//
//	u := bosun.Value[AuthUser](ctx)
//	if u == nil { return Out{}, bosun.E(401, "not signed in", nil) }
func Value[T any](ctx context.Context) *T {
	v, _ := ctx.Value(ctxKey[T]{}).(*T)
	return v
}

// WithRouteValue is a route option that attaches v to every request hitting
// the route, before any middleware runs. Pair it with a registered
// middleware that reads the value via Value[T]:
//
//	type RequiredRoles struct{ Roles []string }
//
//	type HasPermissionMiddleware struct{}
//	func (m *HasPermissionMiddleware) Handle(next http.Handler) http.Handler {
//	    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
//	        need := bosun.Value[RequiredRoles](r.Context())
//	        u    := bosun.Value[AuthUser](r.Context())
//	        if u == nil || need == nil || !hasAny(u.Roles, need.Roles) {
//	            http.Error(w, "forbidden", http.StatusForbidden)
//	            return
//	        }
//	        next.ServeHTTP(w, r)
//	    })
//	}
//	var _ = bosun.Middleware[HasPermissionMiddleware]()
//
//	bosun.Get(r, "/admin", c.Admin,
//	    bosun.Use[RequireAuth](),
//	    bosun.WithRouteValue(&RequiredRoles{Roles: []string{"admin"}}),
//	    bosun.Use[HasPermissionMiddleware](),
//	)
//
// Order matters: WithRouteValue must precede any middleware that reads the
// attached value. Each WithRouteValue call carries its own type — distinct
// T's don't collide.
func WithRouteValue[T any](v *T) MWRef {
	return UseFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(WithValue(r.Context(), v)))
		})
	})
}
