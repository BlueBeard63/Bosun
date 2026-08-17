# Auth and permissions

The most common middleware chain in a real application has two steps: one middleware authenticates the request and attaches a typed user, and the next checks whether that user holds the roles a route requires. This guide builds that chain, then shows the two ways to parameterize the permission check per route.

## Step 1: authenticate and attach the user

The authentication middleware looks up the caller, rejects unauthenticated requests, and stores a typed `*AuthUser` on the context for everything downstream.

```go
type AuthUser struct {
    ID    int
    Roles []string
}

type RequireAuth struct {
    Sessions *SessionStore // injected
}

func (m *RequireAuth) Handle(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        u, err := m.Sessions.Lookup(r.Header.Get("Authorization"))
        if err != nil {
            http.Error(w, "unauthorized", http.StatusUnauthorized)
            return
        }
        next.ServeHTTP(w, r.WithContext(bosun.WithValue(r.Context(), u)))
    })
}

var _ = bosun.Middleware[RequireAuth]()
```

## Step 2: check permissions with a factory

The permission check needs a different set of roles on each route, so it is written as a factory that captures the roles in a fresh closure. `bosun.UseFunc` wraps that closure as a route reference.

```go
func HasPermission(roles ...string) bosun.MWRef {
    return bosun.UseFunc(func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            u := bosun.Value[AuthUser](r.Context())
            if u == nil {
                http.Error(w, "unauthorized", http.StatusUnauthorized)
                return
            }
            for _, want := range roles {
                if slices.Contains(u.Roles, want) {
                    next.ServeHTTP(w, r)
                    return
                }
            }
            http.Error(w, "forbidden", http.StatusForbidden)
        })
    })
}
```

## Step 3: compose at the route

Order matters. `RequireAuth` must run before `HasPermission` so the typed user is on the context when the permission check reads it.

```go
func (c *Admin) Routes(r *bosun.Router) {
    bosun.Get(r, "/wipe", c.Wipe,
        bosun.Use[RequireAuth](),
        HasPermission("admin"),
    )
    bosun.Get(r, "/posts", c.ListPosts,
        bosun.Use[RequireAuth](),
        HasPermission("admin", "editor"), // either role passes
    )
    bosun.Get(r, "/me", c.Me,
        bosun.Use[RequireAuth](), // any signed-in user
    )
}
```

A small helper removes the repetition, and controller-wide auth pairs well with per-route permission checks.

```go
func authed(roles ...string) []bosun.RouteOpt {
    opts := []bosun.RouteOpt{bosun.Use[RequireAuth]()}
    if len(roles) > 0 {
        opts = append(opts, HasPermission(roles...))
    }
    return opts
}

bosun.Get(r, "/admin/wipe", c.Wipe, authed("admin")...)
```

## Alternative: a registered singleton with Configure

If the permission check needs its own injected dependencies, or you simply want it to look like every other `bosun.Use[T]` reference, make it a registered singleton with a `Configure` method. Bosun calls `Configure` once per route at startup with the arguments you pass to `bosun.Use[T](args...)`, and the returned handler closes over them.

```go
type HasPermissionMiddleware struct {
    // Perms *PermissionsService // injected, if needed
}

// Handle is the no-argument fallback. Parameterized middleware usually
// fails closed here, since there is no sensible default.
func (m *HasPermissionMiddleware) Handle(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        http.Error(w, "permissions not configured", http.StatusInternalServerError)
    })
}

func (m *HasPermissionMiddleware) Configure(roles []string) bosun.MiddlewareHandler {
    return bosun.MiddlewareFunc(func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            u := bosun.Value[AuthUser](r.Context())
            if u == nil {
                http.Error(w, "unauthorized", http.StatusUnauthorized)
                return
            }
            for _, want := range roles {
                if slices.Contains(u.Roles, want) {
                    next.ServeHTTP(w, r)
                    return
                }
            }
            http.Error(w, "forbidden", http.StatusForbidden)
        })
    })
}

var _ = bosun.Middleware[HasPermissionMiddleware]()
```

At the route, the per-route arguments go straight into `bosun.Use`.

```go
bosun.Get(r, "/admin/wipe", c.Wipe,
    bosun.Use[RequireAuth](),
    bosun.Use[HasPermissionMiddleware]([]string{"admin"}),
)
```

At startup the framework resolves the singleton, finds `Configure` by reflection, type-checks the arguments against its parameters (converting where possible), and calls it once. A missing `Configure`, a wrong argument count, or a mismatched type all surface from `app.Start()`, never at request time.

## Choosing between the two

Use the factory function when the check has no injected dependencies; it is the least code. Use the registered singleton with `Configure` when the check needs dependencies, or when you want every middleware reference in the codebase to have the same `bosun.Use[T](args)` shape. Both are correct, so pick one per project and stay consistent.
