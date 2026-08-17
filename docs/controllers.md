# Controllers

A controller is a Go struct that owns a group of related routes and the services those routes need. Bosun discovers controllers through a single package-level declaration, so there is no wiring code in `main`. This guide covers declaring a controller, injecting its dependencies, attaching middleware, and its lifecycle. Routing detail lives in two companion pages: [route groups](./routing-groups.md) and [routing internals](./routing-internals.md).

## Declaring a controller

A controller is registered with `bosun.Controller` and implements one method, `Routes`.

```go
type UsersController struct{}

var _ = bosun.Controller[UsersController]("/users")

func (c *UsersController) Routes(r *bosun.Router) {
    bosun.Get(r, "/:id", c.Get)
    bosun.Post(r, "/", c.Create)
}
```

`bosun.Controller[T]("/prefix")` registers `T` and mounts its routes under the prefix. The `var _ =` form runs the registration at package-init time and discards the placeholder return value. Paths inside `Routes` are joined onto the prefix, so the example above mounts `GET /users/:id` and `POST /users/`.

Handlers follow the standard shape, taking a typed request and returning a typed response or an error.

```go
type GetIn struct {
    ID int `path:"id"`
}

type UserOut struct {
    ID   int    `json:"id"`
    Name string `json:"name"`
}

func (c *UsersController) Get(ctx context.Context, req *bosun.Req[GetIn]) (UserOut, error) {
    if req.Body.ID == 0 {
        return UserOut{}, bosun.E(http.StatusBadRequest, "id required", nil)
    }
    return UserOut{ID: req.Body.ID, Name: "Jack"}, nil
}
```

Request binding and response encoding are covered in the [typed handlers guide](./typed-handlers.md).

## Injecting services

Any controller field whose type is registered (through `bosun.Service[T]` or `registry.RegisterInstance[T]`) is injected automatically, whether it is exported or unexported. There is no constructor.

```go
type UsersController struct {
    Users *UserService // injected
    log   *slog.Logger // injected (registered as an instance)
}

var _ = bosun.Controller[UsersController]("/users")
```

Tag a field `inject:"-"` to leave it at its zero value, and note that plain unregistered types are never injected. See the [services guide](./services.md) for how to register dependencies.

## Controller-wide middleware

Middleware passed after the prefix runs on every route the controller mounts.

```go
var _ = bosun.Controller[Admin]("/admin",
    bosun.Use[mw.Logging](),
    bosun.Use[mw.RequireStaff](),
)
```

Per-route middleware is appended to the individual `bosun.Get`/`bosun.Post` call instead. Both are covered in the [middleware guide](./middleware.md).

## Many controllers

Each controller is one struct, usually in its own file. The framework picks up every controller at `bosun.New()`, so there is no central list to maintain. In `main.go` you only need to import the packages that declare them, often with a blank import.

```go
import (
    _ "myapp/users"
    _ "myapp/orgs"
)
```

## Typed and raw handlers on one router

`*bosun.Router` exposes two parallel APIs. The typed generics (`bosun.Get(r, ...)`) give you request binding, audit events, and OpenAPI entries. The raw methods (`r.Get(...)`) hand you plain `net/http` for cases where you need to stream, hijack, or control the `ResponseWriter` directly.

```go
func (c *Files) Routes(r *bosun.Router) {
    bosun.Get(r, "/files/:id", c.Get)     // typed: binding + audit + OpenAPI
    r.Get("/files/:id/stream", c.Stream)  // raw net/http for streaming
}
```

Reach for the raw form only when you need that control; use typed handlers everywhere else. The [files guide](./files.md) shows a full worked example of serving a file with the raw form.

## Lifecycle

If a controller (or any injected service) implements `Init() error`, it runs once at startup after injection completes. Use it for validation or cache warm-up. For shutdown, implement `io.Closer`; `app.Shutdown()` closes controllers and services in reverse dependency order.

```go
func (c *UsersController) Init() error {
    if c.Users == nil {
        return errors.New("UserService missing")
    }
    return nil
}
```

## Mounting controls for the host

An app can override where an imported controller mounts, or disable an entire package of registrations.

```go
app := bosun.New(
    bosun.OverridePrefix[users.UsersController]("/v2/users"),
    bosun.Disable("github.com/amberstack/bosun/audit"),
)
```

`OverridePrefix` is useful for versioning a controller you import from a module. `Disable` skips every `Service`, `Controller`, and `Default` declared under an import path, which is how you uninstall an optional module.
