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
