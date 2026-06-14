# Bosun public API — tour

A walk through every exported symbol you'll actually call when building an
app. Grouped by what you're trying to do, not alphabetical.

## TL;DR — typed vs untyped routes

The single biggest gotcha:

```go
// TYPED — package-level generics. Use these 99% of the time.
bosun.Get(r,    "/users/:id",  c.Get)   // :id and {id} both work
bosun.Post(r,   "/users",      c.Create)
bosun.Put(r,    "/users/{id}", c.Update)
bosun.Delete(r, "/users/{id}", c.Delete)
bosun.Patch(r,  "/users/{id}", c.Patch)

// Handler shape: func(ctx context.Context, req *bosun.Req[In]) (Out, error)
// Out: struct/map/etc → JSON, string → text/plain, []byte → octet-stream,
//      struct{} → no body.
```

```go
// UNTYPED — methods on *Router. Raw net/http, no binding, no audit.
r.Get("/stream",    c.Stream)     // c.Stream must be http.HandlerFunc
r.Post("/upload",   c.Upload)
// Handler shape: func(w http.ResponseWriter, r *http.Request)
```

If you pass a typed handler to `r.Get(...)` you get:

```
cannot use c.Get (value of type func(ctx context.Context,
  req *bosun.Req[any]) (HealthResponse, error)) as http.HandlerFunc value
```

Fix: call `bosun.Get(r, "/", c.Get)` instead.

Use the untyped form only when you need to stream a response, hijack the
connection, or otherwise own the `ResponseWriter` directly.

---

## 1. Building the app

### `bosun.New(opts ...Option) *App`

Create an app. Apply options, then register every `Service` / `Controller`
declared via package-level `var _ = bosun.Service[...]()`.

```go
app := bosun.New()
```

With options:

```go
app := bosun.New(
    bosun.Disable("github.com/amberstack/bosun/audit"),
    bosun.OverridePrefix[users.Controller]("/v2/users"),
)
```

### `(*App).Run(addr string) error`

`Start()` + `http.ListenAndServe`. The normal entrypoint:

```go
log.Fatal(app.Run(":8080"))
```

### `(*App).Start() error`

Apply module defaults, validate the DI graph, mount controller routes. Use
this when you want to call `app.Mux` yourself (e.g. wrap it in TLS, hand it
to a test server):

```go
if err := app.Start(); err != nil { log.Fatal(err) }
srv := httptest.NewServer(app.Mux)
```

### `(*App).Shutdown() error`

Closes registered services in reverse dependency order (anything
implementing `io.Closer`).

### `App.Reg`, `App.Mux`

Public fields. Use `Reg` to inject external instances:

```go
registry.RegisterInstance[*gorm.DB](app.Reg, db)
registry.RegisterInstance[bosun.Auditor](app.Reg, &MyAuditor{})
```

Use `Mux` when you need raw `*http.ServeMux` access after `Start()`.

---

## 2. App options

### `bosun.Disable(pkgPath string) Option`

Uninstall every `Service` / `Controller` / `Default` declared in that
import path. Useful for trimming optional modules:

```go
app := bosun.New(bosun.Disable("github.com/amberstack/bosun/audit"))
```

### `bosun.OverridePrefix[T any](prefix string) Option`

Remount controller `T` under a different prefix than what `Controller[T]`
declared:

```go
app := bosun.New(bosun.OverridePrefix[users.Controller]("/api/v2/users"))
```

---

## 3. Self-registration (package-level `var _ = ...`)

These return `struct{}` so they can be assigned to `_` at package scope.
The side effect is registration; the value is meaningless.

### `bosun.Service[T any]() struct{}`

Register `T` as an injectable singleton. Fields of `T` whose types are
also registered get auto-injected.

```go
type AuthService struct{}
func (s *AuthService) Check(email, pw string) error { ... }

var _ = bosun.Service[AuthService]()
```

### `bosun.Controller[T any](prefix string, mws ...MWRef) struct{}`

Register a controller. `T` must implement `Routes(*bosun.Router)`. The
prefix is prepended to every route the controller mounts.

```go
type UsersController struct {
    Auth *AuthService   // auto-injected
}

func (c *UsersController) Routes(r *bosun.Router) {
    bosun.Get(r,  "/{id}", c.Get)
    bosun.Post(r, "/",     c.Create)
}

var _ = bosun.Controller[UsersController]("/users")
```

With controller-wide middleware:

```go
var _ = bosun.Controller[Admin]("/admin", bosun.Use[mw.RequireStaff]())
```

### `bosun.Middleware[T any]() struct{}`

Register `T` as injectable middleware. `T` must implement
`Handle(http.Handler) http.Handler`. Internally it's a `Service`, but the
extra type-check catches bad signatures at registration time.

```go
type Logging struct{}
func (Logging) Handle(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        log.Printf("%s %s", r.Method, r.URL.Path)
        next.ServeHTTP(w, r)
    })
}

var _ = bosun.Middleware[Logging]()
```

### `bosun.Default[T any](build func() T) struct{}`

Register a fallback provider. Only fires if the host hasn't registered `T`
themselves by the time `Start()` runs. Modules use this to ship overridable
defaults:

```go
var _ = bosun.Default[*Options](func() *Options {
    return &Options{Greeting: "hi"}
})
```

### `bosun.DefaultBind[I any, Impl any]() struct{}`

Bind interface `I` to implementation `Impl` unless the host overrides it.
`Impl` must also be registered (via `Service[Impl]()`).

```go
var _ = bosun.Service[ConsoleAuditor]()
var _ = bosun.DefaultBind[bosun.Auditor, ConsoleAuditor]()
```

### `bosun.DefaultDynamic[T any](build func() *T) struct{}`

Convenience wrapper that registers a `*Dynamic[T]` fallback. See section 7.

---

## 4. Middleware references

### `bosun.Use[T any](args ...any) MWRef`

Type-safe reference to a registered middleware. Pass to controller
declaration, route registration, or `bosun.Errors(...)` siblings.

Without args, `T.Handle(next)` runs for every request:

```go
// Controller-wide:
var _ = bosun.Controller[Admin]("/admin", bosun.Use[mw.RequireStaff]())

// Per-route:
bosun.Post(r, "/login", c.Login, bosun.Use[mw.RateLimit]())
```

With args, `T` must define a `Configure(args...) MiddlewareHandler` method
whose parameter types match the args you supply. The framework calls
`Configure` once at app start; the returned handler closes over those args
for every request on that route:

```go
// Middleware definition:
type HasPermissionMiddleware struct{}
func (m *HasPermissionMiddleware) Handle(next http.Handler) http.Handler { ... }
func (m *HasPermissionMiddleware) Configure(roles []string) bosun.MiddlewareHandler {
    return bosun.MiddlewareFunc(func(next http.Handler) http.Handler { ... })
}
var _ = bosun.Middleware[HasPermissionMiddleware]()

// Per-route use:
bosun.Get(r, "/admin", c.Admin,
    bosun.Use[RequireAuth](),
    bosun.Use[HasPermissionMiddleware]([]string{"admin"}),
)
bosun.Get(r, "/staff", c.Staff,
    bosun.Use[RequireAuth](),
    bosun.Use[HasPermissionMiddleware]([]string{"admin", "editor"}),
)
```

Argument-type mismatches surface at `app.Start()`, not at request time.

See [`middleware.md`](./middleware.md#variant-registered-singleton-with-uset-args)
for the full pattern.

### `bosun.UseFunc(MiddlewareFunc) MWRef`

Inline middleware closure as a `MWRef`. Use this to write factory helpers
that capture per-route parameters:

```go
func HasPermission(roles ...string) bosun.MWRef {
    return bosun.UseFunc(func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            u := bosun.Value[AuthUser](r.Context())
            if u == nil || !hasAny(u.Roles, roles) {
                http.Error(w, "forbidden", http.StatusForbidden); return
            }
            next.ServeHTTP(w, r)
        })
    })
}

bosun.Get(r, "/admin", c.Admin,
    bosun.Use[RequireAuth](),       // singleton: auth + stash *AuthUser
    HasPermission("admin"),         // factory: per-route role check
)
```

`Use[T](args...)` references a shared singleton (parameterized via
`Configure`); `UseFunc` is per-route from an inline closure. Mix them
freely.

### `bosun.WithRouteValue[T any](v *T) MWRef`

Per-route helper that attaches a typed value to the request context
before any subsequent middleware runs. Reach for this when you need to
pass typed data into a middleware that *can't* expose a `Configure`
method (e.g. a third-party middleware you don't own). For your own
middleware, `Use[T](args...)` with a `Configure` method is usually
cleaner.

```go
bosun.Get(r, "/admin", c.Admin,
    bosun.Use[RequireAuth](),
    bosun.WithRouteValue(&Flag{Name: "x"}),
    bosun.Use[ThirdPartyMW](),
)
```

---

## 5. Typed route registration

The five package-level generics:

```go
func Get   [In, Out any](r *Router, path string, h Handler[In, Out], opts ...RouteOpt)
func Post  [In, Out any](r *Router, path string, h Handler[In, Out], opts ...RouteOpt)
func Put   [In, Out any](r *Router, path string, h Handler[In, Out], opts ...RouteOpt)
func Delete[In, Out any](r *Router, path string, h Handler[In, Out], opts ...RouteOpt)
func Patch [In, Out any](r *Router, path string, h Handler[In, Out], opts ...RouteOpt)

// where Handler[In, Out] = func(context.Context, *Req[In]) (Out, error)
```

Type inference works — you don't write the type parameters:

```go
bosun.Get(r, "/things/{id}", c.GetThing)   // In, Out inferred
```

For the full reference on `Req[In]`, body parsing, and tag binding, see
[`typed-handlers.md`](./typed-handlers.md).

### `bosun.Errors(codes ...int) RouteOpt`

Declare additional error status codes a route can return. Used for OpenAPI
generation when runtime-computed statuses can't be seen by source scanning:

```go
bosun.Post(r, "/things", c.Create,
    bosun.Errors(http.StatusConflict, http.StatusGone),
)
```

---

## 6. Errors

### `bosun.E(status int, publicMessage string, cause error) *Error`

Return from a handler for a controlled response. The client sees `status`
and `publicMessage`; `cause` goes to the audit log only (with file:line of
the `E(...)` call).

```go
if err := c.auth.Check(in.Email, in.Password); err != nil {
    return UserOut{}, bosun.E(http.StatusUnauthorized, "invalid credentials", err)
}
```

Any other `error` returned from a handler becomes a `500
"internal server error"` to the client; the original error text is captured
in the audit event but never sent to the client.

---

## 7. Dynamic (hot-reloadable values)

### `bosun.Dynamic[T]`

```go
type Dynamic[T any] struct { ... }

func (d *Dynamic[T]) Get() *T
func (d *Dynamic[T]) Set(v *T)
func (d *Dynamic[T]) OnChange(fn func(*T))
```

Inject `*Dynamic[T]` into a service and call `Get()` per request, so
config reloads apply without rebuilding the service:

```go
type RateLimit struct {
    Opts *bosun.Dynamic[Options]
}

func (m *RateLimit) Handle(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        per := m.Opts.Get().PerMinute   // freshest value
        ...
    })
}
```

### `bosun.DefaultDynamic[T any](build func() *T) struct{}`

Modules use this to ship a hot-reloadable default that hosts can override:

```go
var _ = bosun.DefaultDynamic[Options](func() *Options {
    return &Options{PerMinute: 60}
})
```

---

## 8. Untyped router (escape hatch)

`*bosun.Router` exposes raw `net/http` registration methods. Use these when
you need full control of the response writer (streaming, SSE, hijack,
file downloads).

```go
func (c *Files) Routes(r *bosun.Router) {
    r.Get(   "/download/{id}", c.Download)
    r.Post(  "/upload",        c.Upload)
    r.Put(   "/replace/{id}",  c.Replace)
    r.Delete("/remove/{id}",   c.Remove)
    r.Patch( "/patch/{id}",    c.Patch)
}

// signature: func(w http.ResponseWriter, r *http.Request)
func (c *Files) Download(w http.ResponseWriter, r *http.Request) {
    id := r.PathValue("id")
    f, err := os.Open(c.path(id))
    if err != nil { http.Error(w, "not found", 404); return }
    defer f.Close()
    io.Copy(w, f)
}
```

Per-route middleware works the same way as for typed routes:

```go
r.Get("/admin/dump", c.Dump, bosun.Use[mw.RequireStaff]())
```

You give up audit events, OpenAPI generation, and typed binding. You keep
full control over status, body bytes, headers, and the connection.

---

## 9. Auditing

### `bosun.Auditor` interface

```go
type Auditor interface {
    Audit(ctx context.Context, ev AuditEvent)
}
```

Implement and register an auditor to capture every typed request:

```go
type DBAuditor struct{ db *gorm.DB }

func (a *DBAuditor) Audit(ctx context.Context, ev bosun.AuditEvent) {
    a.db.Create(&AuditRow{
        At: ev.Time, Method: ev.Method, Path: ev.Path,
        Status: ev.Status, Err: ev.Err,
    })
}

registry.RegisterInstance[bosun.Auditor](app.Reg, &DBAuditor{db: db})
```

### `bosun.AuditEvent`

Captured per typed request. Body and response are redacted snapshots —
sensitive field names and `audit:"-"` tagged fields are replaced with
`"[REDACTED]"`. See `typed-handlers.md` for redaction rules.

### `bosun.Redact(v any) any`

The same redacting walker used internally. Use it if your own logging
needs the same redaction behavior.

---

## 10. Route inspection

### `bosun.TypedRoutes() []RouteInfo`

Snapshot of every typed route registered so far. Complete after
`app.Start()`. OpenAPI tooling reads this.

```go
for _, ri := range bosun.TypedRoutes() {
    fmt.Printf("%-6s %s  %s\n", ri.Method, ri.Path, ri.Handler)
}
```

`RouteInfo` fields: `Method`, `Path`, `Handler`, `In reflect.Type`,
`Out reflect.Type`, `Declared []int`.

---

## 11. Interfaces you implement

### `bosun.BaseController`

```go
type BaseController interface {
    Routes(r *Router)
}
```

Every controller implements this. `bosun.Controller[T]` panics at
registration if `T` doesn't.

### `bosun.MiddlewareHandler`

```go
type MiddlewareHandler interface {
    Handle(next http.Handler) http.Handler
}
```

Every middleware implements this. `bosun.Middleware[T]` panics at
registration if `T` doesn't.

---

## Putting it together

A minimal end-to-end app:

```go
package main

import (
    "context"
    "log"
    "net/http"

    "github.com/amberstack/bosun"
)

type HealthResponse struct {
    OK bool `json:"ok"`
}

type HealthController struct{}

var _ = bosun.Controller[HealthController]("")

func (c *HealthController) Routes(r *bosun.Router) {
    bosun.Get(r, "/health", c.Health)
}

func (c *HealthController) Health(ctx context.Context, _ *bosun.Req[struct{}]) (HealthResponse, error) {
    return HealthResponse{OK: true}, nil
}

func main() {
    app := bosun.New()
    log.Fatal(app.Run(":8080"))
}
```

Note `*bosun.Req[struct{}]` for a handler that takes no input. If you'd
rather pass loose JSON through, use `*bosun.Req[any]`. For a typed body,
declare a struct with `json:` / `path:` / `query:` / `header:` / `form:`
tags — see [`typed-handlers.md`](./typed-handlers.md).
