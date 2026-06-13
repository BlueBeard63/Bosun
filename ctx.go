package bosun

import "context"

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
