# Middleware

Middleware wraps `http.Handler`. In Bosun, middleware is a registered type
with optional injected dependencies, referenced by `bosun.Use[T]()`.

---

## Basics

### Declare middleware

A middleware is any type that implements `Handle(next http.Handler) http.Handler`:

```go
type Logging struct{}

func (m *Logging) Handle(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        log.Printf("%s %s", r.Method, r.URL.Path)
        next.ServeHTTP(w, r)
    })
}

var _ = bosun.Middleware[Logging]()
```

`bosun.Middleware[T]()` is `bosun.Service[T]()` plus a compile-time check
that `T` satisfies `MiddlewareHandler`. If it doesn't, registration panics
with a clear message at app start.

### Attach it

Controller-wide:

```go
var _ = bosun.Controller[UsersController]("/users", bosun.Use[Logging]())
```

Per-route:

```go
func (c *UsersController) Routes(r *bosun.Router) {
    bosun.Post(r, "/login", c.Login, bosun.Use[RateLimit]())
}
```

Order: app → controller → per-route → handler. Each layer wraps the next,
outermost first.

---

## Middleware with dependencies

Middleware is a service. Inject anything you need:

```go
type RequireAuth struct {
    Auth *AuthService     // injected
}

func (m *RequireAuth) Handle(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        tok := r.Header.Get("Authorization")
        if !m.Auth.Valid(tok) {
            http.Error(w, "forbidden", http.StatusForbidden)
            return
        }
        next.ServeHTTP(w, r)
    })
}

var _ = bosun.Middleware[RequireAuth]()
```

`Init()` and `Close()` work the same way they do for services.

---

## Passing data to handlers — typed context values

This is the killer pattern: an auth middleware that rejects unauthorized
requests with `403`, *and* makes the authenticated user available to
downstream handlers without any string keys or type assertions.

### From the middleware: attach a typed value

```go
type AuthUser struct {
    ID    int
    Name  string
    Roles []string
}

type RequireAuth struct {
    Sessions *SessionStore
}

func (m *RequireAuth) Handle(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        u, err := m.Sessions.Lookup(r.Header.Get("Authorization"))
        if err != nil {
            http.Error(w, "forbidden", http.StatusForbidden)
            return
        }
        ctx := bosun.WithValue(r.Context(), u)   // u is *AuthUser
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}

var _ = bosun.Middleware[RequireAuth]()
```

### From the handler: pull it out type-safely

```go
func (c *Me) Get(ctx context.Context, _ *bosun.Req[struct{}]) (UserOut, error) {
    u := bosun.Value[AuthUser](ctx)
    if u == nil {
        // Shouldn't happen if RequireAuth ran, but defensive:
        return UserOut{}, bosun.E(http.StatusUnauthorized, "not signed in", nil)
    }
    return UserOut{Name: u.Name}, nil
}
```

The key is the type itself — `bosun.Value[AuthUser]` and
`bosun.Value[Session]` get different slots automatically. No string keys,
no collisions, no untyped `ctx.Value("user").(*User)`.

### Stacking multiple typed values

Middleware can layer:

```go
func (m *Tenant) Handle(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        u := bosun.Value[AuthUser](r.Context())
        if u == nil {
            http.Error(w, "unauthorized", 401); return
        }
        org := m.Orgs.For(u)
        ctx := bosun.WithValue(r.Context(), org)   // *Org
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}
```

Handlers downstream pull either or both:

```go
u   := bosun.Value[AuthUser](ctx)
org := bosun.Value[Org](ctx)
```

---

## Built-in middleware

The `mw` package ships ready-to-use middleware:

```go
import "github.com/amberstack/bosun/mw"

bosun.Use[mw.Logging]()      // slog-based request logger
bosun.Use[mw.RateLimit]()    // per-IP, hot-reloadable via bosun.Dynamic[RateLimitOptions]
```

Configure `RateLimit`:

```go
registry.RegisterInstance[*bosun.Dynamic[mw.RateLimitOptions]](app.Reg, ...)
```

…or rely on the bundled `bosun.DefaultDynamic[RateLimitOptions]` and tune
via the config module at runtime.

---

## Chaining: Auth → HasPermission(roles…)

The most common chain pattern in real apps: one middleware authenticates
the request and stashes a typed user, the next checks role/permission,
each route declares which permissions it requires. Bosun supports this
with two complementary tools:

1. **Registered singleton middleware** for shared, parameterless steps
   (`bosun.Use[Auth]()`).
2. **Inline factory middleware** for per-route parameters
   (`bosun.UseFunc(...)`, usually wrapped in a helper like
   `HasPermission("admin", "editor")`).

### Step 1 — Auth attaches a typed user

```go
type AuthUser struct {
    ID    int
    Roles []string
}

type RequireAuth struct {
    Sessions *SessionStore   // injected
}

func (m *RequireAuth) Handle(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        u, err := m.Sessions.Lookup(r.Header.Get("Authorization"))
        if err != nil {
            http.Error(w, "unauthorized", http.StatusUnauthorized)
            return
        }
        ctx := bosun.WithValue(r.Context(), u)   // *AuthUser
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}

var _ = bosun.Middleware[RequireAuth]()
```

### Step 2 — HasPermission factory captures the required roles

`bosun.UseFunc` wraps an inline closure as a `MWRef`. Each call returns a
fresh closure, so the roles are baked into that route only:

```go
func HasPermission(roles ...string) bosun.MWRef {
    return bosun.UseFunc(func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            u := bosun.Value[AuthUser](r.Context())
            if u == nil {
                // RequireAuth must run before this — if it didn't, fail closed.
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

### Step 3 — Compose at the route declaration

```go
func (c *Admin) Routes(r *bosun.Router) {
    bosun.Get(r, "/wipe", c.Wipe,
        bosun.Use[RequireAuth](),
        HasPermission("admin"),
    )
    bosun.Get(r, "/posts", c.ListPosts,
        bosun.Use[RequireAuth](),
        HasPermission("admin", "editor"),   // either role passes
    )
    bosun.Get(r, "/me", c.Me,
        bosun.Use[RequireAuth](),           // any signed-in user
    )
}
```

Order matters: middleware runs left-to-right, outermost first. `RequireAuth`
*must* precede `HasPermission` so the typed user is in the context when
the permission check looks it up.

### Step 4 — Handlers use the same typed user

```go
func (c *Admin) Me(ctx context.Context, _ *bosun.Req[struct{}]) (MeOut, error) {
    u := bosun.Value[AuthUser](ctx)
    return MeOut{ID: u.ID, Roles: u.Roles}, nil
}
```

The handler reads the same `*AuthUser` the middleware put in. No string
keys, no untyped assertions, no plumbing.

### Pulling out boilerplate

Most apps end up with a helper that bundles the common chain:

```go
func authed(roles ...string) []bosun.RouteOpt {
    opts := []bosun.RouteOpt{bosun.Use[RequireAuth]()}
    if len(roles) > 0 {
        opts = append(opts, HasPermission(roles...))
    }
    return opts
}

bosun.Get(r, "/admin/wipe",  c.Wipe,    authed("admin")...)
bosun.Get(r, "/admin/posts", c.Posts,   authed("admin", "editor")...)
bosun.Get(r, "/me",          c.Me,     authed()...)
```

For controller-wide auth with per-route permissions:

```go
var _ = bosun.Controller[Admin]("/admin", bosun.Use[RequireAuth]())

func (c *Admin) Routes(r *bosun.Router) {
    // RequireAuth already applied controller-wide; add per-route role checks
    bosun.Get(r, "/wipe",  c.Wipe,  HasPermission("admin"))
    bosun.Get(r, "/posts", c.Posts, HasPermission("admin", "editor"))
}
```

### Why `UseFunc` for factories

`bosun.Use[T]()` references a registered singleton — fine when middleware
has no per-route parameters. The moment you want parameters
(`HasPermission("admin")`), you need a fresh closure per call site, which
is exactly what `UseFunc` provides.

You can also pass a singleton's parameters via a registered options
struct (Pattern 2 in [`service-options.md`](./service-options.md)) — but
that gives every route the same value. The factory pattern is right when
different routes need different parameters.

### Variant: registered singleton with `Use[T](args...)`

If you want `HasPermission` to be a `bosun.Middleware[T]` (because it
needs injected dependencies, or just for symmetry with the rest of your
middleware), pass the per-route args directly to `bosun.Use[T](...)`. The
singleton exposes a `Configure(...)` method whose parameters match the
args you supply at the call site.

```go
package middleware

import (
    "net/http"
    "slices"

    "github.com/amberstack/bosun"
)

// Registered singleton. Inject deps here as needed.
type HasPermissionMiddleware struct {
    // Perms *PermissionsService   // injected — example
}

// Handle is the no-args fallback. Most parameterized middleware fail
// closed here: there's no sensible default if no roles were declared.
func (m *HasPermissionMiddleware) Handle(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        http.Error(w, "permissions not configured", http.StatusInternalServerError)
    })
}

// Configure is called once per route at app.Start() with the args you
// passed to bosun.Use[HasPermissionMiddleware](...). The returned
// MiddlewareHandler closes over those args for every request to that
// route.
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

Then at the route — args go right into `Use[T](...)`:

```go
bosun.Get(r, "/admin/wipe", c.Wipe,
    bosun.Use[RequireAuth](),
    bosun.Use[middleware.HasPermissionMiddleware]([]string{"admin"}),
)

bosun.Get(r, "/admin/posts", c.Posts,
    bosun.Use[RequireAuth](),
    bosun.Use[middleware.HasPermissionMiddleware]([]string{"admin", "editor"}),
)
```

That's it — same `Use[T]` call you write everywhere else, just with
per-route arguments.

#### How `Use[T](args...)` finds the right method

When you write `bosun.Use[T](a, b, c)`:

1. At app start, the framework resolves the singleton `*T` from the registry.
2. It looks up `T.Configure` via reflection.
3. It type-checks the supplied args against `Configure`'s parameter types.
   Convertible types (e.g. untyped string literal → `string`) are converted automatically.
4. It calls `Configure(a, b, c)` once. The returned `MiddlewareHandler`
   closes over the args and runs for every request on that route.

Errors — missing `Configure`, wrong arg count, wrong arg type — surface
from `app.Start()`. Nothing fails at request time.

#### Configure with multiple args

`Configure` is just a regular method; pass anything you want.

```go
type RateLimitMW struct{}

func (m *RateLimitMW) Configure(perMinute int, burst int) bosun.MiddlewareHandler {
    return bosun.MiddlewareFunc(func(next http.Handler) http.Handler { ... })
}

// usage:
bosun.Use[RateLimitMW](60, 10)
```

The framework passes positional args to `Configure` in order.

#### When `Use[T](args...)` won't work

- `T` has no `Configure` method. → Caught at `app.Start()`.
- `Configure` returns something other than `bosun.MiddlewareHandler`. → Caught at `app.Start()`.
- Number / types of args don't line up with `Configure`'s parameters. → Caught at `app.Start()`.

If you only ever call `Use[T]()` (no args), `Configure` isn't required —
`Handle` runs as before.

### Which to pick — factory function vs singleton?

| Question                                                | Pick this                                                |
| ------------------------------------------------------- | -------------------------------------------------------- |
| Does the check have **no injected dependencies**?       | Factory function — one helper, no struct, no `Configure` |
| Does the check need injected deps (DB, cache, etc.)?    | Singleton with `Configure(...)` — DI works as normal     |
| Do you want the API to look like every other middleware? | Singleton with `Configure(...)` — `bosun.Use[X](args)` is the same shape as all your other middleware refs |
| Do you want the least code?                              | Factory function                                         |

There's no wrong answer. The factory form is shorter; the singleton form
is more consistent with the rest of your middleware. Pick per project
and stay consistent.

---

## Advanced

### Conditional short-circuit

Standard pattern: short-circuit with `http.Error` or a JSON body, then
`return` without calling `next.ServeHTTP`.

```go
if !m.allow(r) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusForbidden)
    json.NewEncoder(w).Encode(map[string]string{"error": "forbidden"})
    return
}
next.ServeHTTP(w, r)
```

Note: short-circuited responses don't emit an audit event (the typed
adapter only runs when the request reaches the handler). If you want to
audit denials, log from the middleware itself.

### Recording the response status

If your middleware wants the status code, wrap the `ResponseWriter`:

```go
type statusRec struct {
    http.ResponseWriter
    status int
}
func (r *statusRec) WriteHeader(c int) { r.status = c; r.ResponseWriter.WriteHeader(c) }

func (m *Logging) Handle(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        rec := &statusRec{ResponseWriter: w, status: 200}
        next.ServeHTTP(rec, r)
        log.Printf("status=%d", rec.status)
    })
}
```

This is how `mw.Logging` is implemented (see `mw/logging.go`).

### Replacing the request context

Always use `r.WithContext(ctx)` and pass the new request to `next`:

```go
ctx := bosun.WithValue(r.Context(), user)
next.ServeHTTP(w, r.WithContext(ctx))   // ← new req
```

Mutating `r.Context()` directly doesn't work — `context.Context` is
immutable; you replace it via the wrapper.

### Hot-reloadable middleware config

Inject a `*bosun.Dynamic[Options]` and read it per-request:

```go
type RateLimit struct {
    Opts *bosun.Dynamic[Options]
}

func (m *RateLimit) Handle(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        limit := m.Opts.Get().PerMinute   // fresh every request
        ...
    })
}
```

See `services.md` § "Hot-reloadable values".

### Ordering & layering

The execution order for a request is:

```
controller-wide MW (in declared order, outermost first)
  → per-route MW (in declared order)
    → typed adapter (bind + invoke + audit)
      → your handler
```

If route `bosun.Post(r, "/x", h, bosun.Use[A](), bosun.Use[B]())` is on a
controller with `bosun.Use[Z]()`, the actual call chain is:
`Z → A → B → adapter → h`.

### Per-request initialization quirks

`Handle(next)` is called once at mount time, not per request. The
`http.HandlerFunc` you return is what runs per request. Anything you set
up inside `Handle` (closures, recorders, locks) needs to be created inside
the returned `HandlerFunc`, not at the outer level.

```go
func (m *Logging) Handle(next http.Handler) http.Handler {
    // once per mount — not per request
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // once per request — fresh start, fresh recorder, fresh ctx
        start := time.Now()
        next.ServeHTTP(w, r)
        m.log.Info("done", "ms", time.Since(start).Milliseconds())
    })
}
```

### Untyped routes get the same middleware

`r.Get` (the raw escape hatch) runs through the same middleware chain.
Auth/logging works identically whether the underlying handler is a typed
`bosun.Get(r, ...)` or a raw `r.Get(...)`.
