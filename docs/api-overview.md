# Bosun public API

This is a tour of every exported symbol you call when building an app, grouped by task rather than alphabetically. For deep detail on any area, follow the links to its dedicated guide.

## Typed and untyped routes

The most important distinction is between typed and untyped routes. The typed generics (`bosun.Get`, `Post`, `Put`, `Delete`, `Patch`) take a handler of the form `func(ctx context.Context, req *bosun.Req[In]) (Out, error)` and give you body binding, audit events, and OpenAPI entries; use them for almost everything. The untyped methods on `*Router` (`r.Get`, and so on) take a plain `func(w http.ResponseWriter, r *http.Request)` and hand you raw `net/http` for streaming, hijacking, or full control of the response. Passing a typed handler to `r.Get` is a compile error; call `bosun.Get(r, ...)` instead.

```go
bosun.Get(r, "/users/:id", c.Get)  // typed; :id and {id} both work
r.Get("/stream", c.Stream)         // untyped; c.Stream is an http.HandlerFunc
```

## Building the app

`bosun.New(opts ...Option) *App` creates an app and applies every package-level registration. `(*App).Run(addr) error` is `Start()` followed by `ListenAndServe` and is the normal entry point. `(*App).Start() error` applies module defaults, validates the dependency graph, and mounts routes, which you call directly when you want to serve `app.Mux` yourself. `(*App).Shutdown() error` closes registered `io.Closer` services in reverse dependency order. The public fields `App.Reg` and `App.Mux` give you the registry (for registering external instances) and the underlying `*http.ServeMux`.

```go
app := bosun.New()
registry.RegisterInstance[*gorm.DB](app.Reg, db)
log.Fatal(app.Run(":8080"))
```

Two options shape an app at construction. `bosun.Disable(pkgPath)` uninstalls every registration under an import path, and `bosun.OverridePrefix[T](prefix)` remounts a controller at a different prefix than it declared.

## Self-registration

These functions run at package-init time through `var _ = ...` and return a meaningless `struct{}`; the value is discarded and the side effect is the registration. `bosun.Service[T]()` registers an injectable singleton. `bosun.Controller[T](prefix, mws...)` registers a controller, which must implement `Routes(*bosun.Router)`. `bosun.Middleware[T]()` registers middleware, which must implement `Handle(http.Handler) http.Handler`. `bosun.Default[T](build)` ships a fallback provider that fires only if the host has not registered `T`. `bosun.DefaultBind[I, Impl]()` binds an interface to an implementation unless the host overrides it. `bosun.DefaultDynamic[T](build)` ships a hot-reloadable default. These are covered in the [services](./services.md) and [service options](./service-options.md) guides.

## Middleware references

`bosun.Use[T](args ...any) MWRef` is a type-safe reference to a registered middleware. With no arguments it runs `T.Handle`; with arguments, `T` must define a `Configure(args...) MiddlewareHandler` method that the framework calls once at startup. `bosun.UseFunc(MiddlewareFunc) MWRef` wraps an inline closure, which is how per-route factory helpers like `HasPermission("admin")` are built. `bosun.WithRouteValue[T](v *T) MWRef` attaches a typed value to the request context before later middleware runs, for passing data into a middleware you cannot modify. The [auth and permissions](./auth-and-permissions.md) guide works through these in a real chain.

## Typed routes and errors

The five typed generics share the signature `func[In, Out any](r *Router, path string, h Handler[In, Out], opts ...RouteOpt)`, and type inference means you never write the type parameters. `bosun.Errors(codes ...int) RouteOpt` declares extra status codes for OpenAPI. `bosun.E(status, publicMessage, cause) *Error` returns a controlled error: the client sees the status and message while the cause is recorded for the audit log with the call site. Any other error becomes a 500. The [typed handlers](./typed-handlers.md) and [errors](./errors.md) guides cover these fully.

## Context values and dynamic values

`bosun.WithValue[T](ctx, *T)` and `bosun.Value[T](ctx) *T` pass typed values from middleware to handlers, keyed by the Go type so there are no collisions. `bosun.Dynamic[T]` holds a hot-reloadable value with `Get()`, `Set(v)`, and `OnChange(fn)`; inject `*bosun.Dynamic[T]` and call `Get()` per request so configuration reloads apply without rebuilding the service. The [config guide](./config.md) drives dynamic values from files, environment, and databases.

## Auditing and inspection

`bosun.Auditor` is the interface you implement and register to receive an `AuditEvent` for every typed request, with redacted snapshots of the body and response. `bosun.Redact(v any) any` is the same redacting walker, available for your own logging. `bosun.TypedRoutes() []RouteInfo` returns every typed route after `Start()`, where each `RouteInfo` carries the method, path, handler name, input and output types, and declared statuses; this is what OpenAPI generation and the deploy manifest read.

## Interfaces you implement

Two interfaces are contracts the framework checks. `bosun.BaseController` requires `Routes(r *Router)`, and every controller satisfies it. `bosun.MiddlewareHandler` requires `Handle(next http.Handler) http.Handler`, and every middleware satisfies it. Registration panics early if a type does not implement the interface it was registered as.

## A minimal app

```go
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
    log.Fatal(bosun.New().Run(":8080"))
}
```
