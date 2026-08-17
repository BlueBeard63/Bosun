# Getting started

This tutorial walks you from an empty directory to a running Bosun service that has routing, dependency injection, a database, middleware, and error handling. Follow it top to bottom; by the end you will understand the shape of every Bosun app and be ready to read the how-to guides for each feature.

<figure class="diagram">
<svg viewBox="0 0 720 170" role="img" aria-labelledby="gs-title gs-desc" xmlns="http://www.w3.org/2000/svg">
<title id="gs-title">How a Bosun app boots</title>
<desc id="gs-desc">Package-init declarations are collected by New, validated and wired by Start, and served on the Mux by Run.</desc>
<defs>
<marker id="gs-arw" markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto"><polygon points="0 0, 8 3, 0 6" fill="var(--fg-muted)"/></marker>
</defs>
<line x1="172" y1="64" x2="192" y2="64" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#gs-arw)"/>
<line x1="346" y1="64" x2="366" y2="64" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#gs-arw)"/>
<line x1="520" y1="64" x2="540" y2="64" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#gs-arw)"/>
<rect x="20" y="32" width="150" height="64" rx="6" fill="var(--code-bg)" stroke="var(--fg-muted)" stroke-width="1"/>
<text x="95" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Registrations</text>
<text x="95" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">package-init decls</text>
<rect x="194" y="32" width="150" height="64" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="269" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">New()</text>
<text x="269" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">build registry</text>
<rect x="368" y="32" width="150" height="64" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="443" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Start()</text>
<text x="443" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">validate + mount</text>
<rect x="542" y="32" width="150" height="64" rx="6" fill="var(--accent-soft)" stroke="var(--accent)" stroke-width="1"/>
<text x="617" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--accent)">Run()</text>
<text x="617" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">serve on Mux</text>
</svg>
<figcaption>Controllers and services register themselves at package init; New collects them, Start validates and mounts them, and Run serves.</figcaption>
</figure>

## Before you start

You need Go 1.22 or newer, because Bosun builds on the pattern-based routing added to the standard library `net/http` in that release.

Install the framework into a new module:

```bash
go mod init example.com/hello
go get github.com/amberstack/bosun
```

## Step 1: your first route

Create `main.go`. A Bosun app is a set of controllers, each of which owns a group of routes. The smallest possible app has one controller with one route.

```go
package main

import (
    "context"
    "log"

    "github.com/amberstack/bosun"
)

type HealthController struct{}

var _ = bosun.Controller[HealthController]("")

func (c *HealthController) Routes(r *bosun.Router) {
    bosun.Get(r, "/health", c.Health)
}

type HealthOut struct {
    OK bool `json:"ok"`
}

func (c *HealthController) Health(ctx context.Context, _ *bosun.Req[struct{}]) (HealthOut, error) {
    return HealthOut{OK: true}, nil
}

func main() {
    log.Fatal(bosun.New().Run(":8080"))
}
```

Run it and call the route:

```bash
go run .
curl http://localhost:8080/health
# {"ok":true}
```

Four things happened, one per key line:

- `var _ = bosun.Controller[HealthController]("")` registers the controller at package-init time. The `""` argument is the path prefix (empty here), and `var _ =` discards the placeholder return value.
- `Routes(r *bosun.Router)` is the one method every controller implements. Inside it you mount routes with the typed generics `bosun.Get`, `bosun.Post`, and so on.
- The handler signature is always `func(ctx context.Context, req *bosun.Req[In]) (Out, error)`. Here `In` is `struct{}` (no request body) and `Out` is `HealthOut`, which Bosun encodes as JSON.
- `bosun.New().Run(":8080")` discovers every registered controller, validates the dependency graph, and starts listening. That is the entire `main`.

## Step 2: inject a service

Most application code lives in services rather than controllers. A service is any type you register with `bosun.Service`, and any field whose type is also registered is filled in for you.

```go
type Greeter struct{}

func (g *Greeter) Hello(name string) string { return "hello, " + name }

var _ = bosun.Service[Greeter]()

type HelloController struct {
    Greeter *Greeter // injected by type
}

var _ = bosun.Controller[HelloController]("/hello")

func (c *HelloController) Routes(r *bosun.Router) {
    bosun.Get(r, "/:name", c.Hello)
}

type HelloIn struct {
    Name string `path:"name"`
}

type HelloOut struct {
    Msg string `json:"msg"`
}

func (c *HelloController) Hello(ctx context.Context, req *bosun.Req[HelloIn]) (HelloOut, error) {
    return HelloOut{Msg: c.Greeter.Hello(req.Body.Name)}, nil
}
```

Calling `curl http://localhost:8080/hello/world` returns `{"msg":"hello, world"}`.

There is no constructor and no wiring code. Because `*Greeter` is a registered type, the framework populates the `Greeter` field when it builds `*HelloController`. The `path:"name"` tag binds the `:name` path segment into the request struct.

## Step 3: add a database

External dependencies that you construct yourself (database handles, third-party clients) are registered as instances on the app registry before the app starts.

```go
import (
    "github.com/amberstack/bosun/registry"
    "gorm.io/driver/sqlite"
    "gorm.io/gorm"
)

func main() {
    db, _ := gorm.Open(sqlite.Open("app.db"))
    db.AutoMigrate(&User{})

    app := bosun.New()
    registry.RegisterInstance[*gorm.DB](app.Reg, db)
    log.Fatal(app.Run(":8080"))
}
```

Any service with a `*gorm.DB` field now receives the live handle. The full data-access pattern lives in the [GORM guide](./database-gorm.md) and the driver-agnostic [Repo guide](./repo.md).

## Step 4: add middleware

Middleware wraps handlers to add cross-cutting behavior. Attach a built-in like request logging to a single route:

```go
import "github.com/amberstack/bosun/mw"

func (c *HelloController) Routes(r *bosun.Router) {
    bosun.Get(r, "/:name", c.Hello, bosun.Use[mw.Logging]())
}
```

Or apply it to every route on the controller:

```go
var _ = bosun.Controller[HelloController]("/hello", bosun.Use[mw.Logging]())
```

Writing your own middleware is covered in the [middleware guide](./middleware.md).

## Step 5: return errors

Return `bosun.E` to send a controlled error. The client sees the status code and the public message; the cause is recorded for the audit log but never sent to the client.

```go
import "net/http"

func (c *HelloController) Hello(ctx context.Context, req *bosun.Req[HelloIn]) (HelloOut, error) {
    if req.Body.Name == "" {
        return HelloOut{}, bosun.E(http.StatusBadRequest, "name is required", nil)
    }
    return HelloOut{Msg: c.Greeter.Hello(req.Body.Name)}, nil
}
```

## Where to go next

You now have a service with routing, injection, a database, middleware, and errors. Continue with the guide that matches your next task:

- [Typed handlers](./typed-handlers.md) explains request binding and response encoding in depth.
- [Controllers](./controllers.md) and [Services](./services.md) cover routing and dependency injection from basics to advanced use.
- [Middleware](./middleware.md) shows authentication and typed context values.
- [Forms](./forms.md) and [Files](./files.md) handle non-JSON input.
- [API overview](./api-overview.md) is the reference for every public symbol.
