# Middleware

Middleware wraps an `http.Handler` to add cross-cutting behavior such as logging, authentication, or rate limiting. In Bosun a middleware is a registered type with optional injected dependencies, referenced at a route with `bosun.Use[T]()`. This guide covers writing middleware, attaching it, passing typed values to handlers, and the built-ins. The full authentication-and-permissions chain has its own page: [auth and permissions](./auth-and-permissions.md).

## Writing middleware

A middleware is any type that implements `Handle(next http.Handler) http.Handler`.

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

`bosun.Middleware[T]()` is `bosun.Service[T]()` plus a compile-time check that `T` satisfies the middleware contract; if it does not, registration panics with a clear message at startup.

## Attaching middleware

Attach middleware controller-wide by passing it after the prefix, or per-route by appending it to the handler registration.

```go
var _ = bosun.Controller[UsersController]("/users", bosun.Use[Logging]())

func (c *UsersController) Routes(r *bosun.Router) {
    bosun.Post(r, "/login", c.Login, bosun.Use[RateLimit]())
}
```

The chain runs outermost-first: controller-wide middleware, then per-route middleware, then the handler. Ordering is covered in [routing internals](./routing-internals.md).

## Middleware with dependencies

Because middleware is a service, it can inject anything the registry knows about, and it may implement `Init()` and `Close()` just like any other service.

```go
type RequireAuth struct {
    Auth *AuthService // injected
}

func (m *RequireAuth) Handle(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if !m.Auth.Valid(r.Header.Get("Authorization")) {
            http.Error(w, "forbidden", http.StatusForbidden)
            return
        }
        next.ServeHTTP(w, r)
    })
}

var _ = bosun.Middleware[RequireAuth]()
```

## Passing typed values to handlers

Middleware communicates with downstream handlers through typed context values. The key is the Go type itself, so there are no string keys and no type assertions. A middleware attaches a value with `bosun.WithValue`, and a handler reads it with `bosun.Value`.

```go
type AuthUser struct {
    ID   int
    Name string
}

func (m *RequireAuth) Handle(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        u, err := m.Sessions.Lookup(r.Header.Get("Authorization"))
        if err != nil {
            http.Error(w, "forbidden", http.StatusForbidden)
            return
        }
        next.ServeHTTP(w, r.WithContext(bosun.WithValue(r.Context(), u))) // u is *AuthUser
    })
}
```

```go
func (c *Me) Get(ctx context.Context, _ *bosun.Req[struct{}]) (UserOut, error) {
    u := bosun.Value[AuthUser](ctx)
    if u == nil {
        return UserOut{}, bosun.E(http.StatusUnauthorized, "not signed in", nil)
    }
    return UserOut{Name: u.Name}, nil
}
```

Because each type gets its own slot, `bosun.Value[AuthUser]` and `bosun.Value[Org]` never collide, and middleware can stack several values that handlers read independently.

## Built-in middleware

The `mw` package ships ready-to-use middleware.

```go
import "github.com/amberstack/bosun/mw"

bosun.Use[mw.Logging]()      // slog-based request logger
bosun.Use[mw.RateLimit]()    // per-IP limit, hot-reloadable
bosun.Use[mw.Correlation]()  // attaches a correlation id to every request
```

`RateLimit` reads its limit from a `*bosun.Dynamic[mw.RateLimitOptions]`, so you can tune it at runtime through the [config module](./config.md). `Correlation` is described in the [tracing guide](./tracing.md).

## Short-circuiting a request

To reject a request, write the response and return without calling `next.ServeHTTP`.

```go
if !m.allow(r) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusForbidden)
    json.NewEncoder(w).Encode(map[string]string{"error": "forbidden"})
    return
}
next.ServeHTTP(w, r)
```

A short-circuited response does not emit an audit event, because the typed adapter only runs when the request reaches the handler. To record denials, log them from the middleware itself.

## Recording the response status

To observe the status code, wrap the `ResponseWriter`. This is exactly how `mw.Logging` is implemented.

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

## Per-request versus per-mount work

`Handle(next)` is called once when the route is mounted, not per request. The `http.HandlerFunc` it returns is what runs for each request, so anything request-scoped (timers, recorders) must be created inside that returned function.

```go
func (m *Logging) Handle(next http.Handler) http.Handler {
    // runs once at mount time
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        start := time.Now() // runs per request
        next.ServeHTTP(w, r)
        m.log.Info("done", "ms", time.Since(start).Milliseconds())
    })
}
```

## Hot-reloadable configuration

Inject a `*bosun.Dynamic[Options]` and read it per request so configuration changes apply to live traffic. This pattern is described in the [services guide](./services.md).

```go
type RateLimit struct {
    Opts *bosun.Dynamic[Options]
}

func (m *RateLimit) Handle(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        limit := m.Opts.Get().PerMinute // fresh every request
        ...
    })
}
```
