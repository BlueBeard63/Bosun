# Getting started

The smallest end-to-end Bosun app, with notes on every line.

---

## Install

```bash
go get github.com/amberstack/bosun
```

Go 1.22+ (Bosun uses the new `net/http` routing).

---

## Hello, world

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

Run it:

```bash
go run .
curl http://localhost:8080/health
# {"ok":true}
```

### Line by line

- `var _ = bosun.Controller[HealthController]("")` — registers the
  controller at package init. The `""` is the path prefix (empty here).
  `var _ =` just discards the meaningless `struct{}` return.
- `Routes(r *bosun.Router)` — every controller implements this. Inside,
  you mount routes against the typed `bosun.Get/Post/...` generics.
- Handler signature is always `func(ctx, req *bosun.Req[In]) (Out, error)`.
  Here `In = struct{}` (no body), `Out = HealthOut` (JSON-encoded).
- `bosun.New().Run(":8080")` — discovers controllers, validates the DI
  graph, listens. That's the entire main.

---

## Add a service with injection

```go
type Greeter struct{}

func (g *Greeter) Hello(name string) string { return "hello, " + name }

var _ = bosun.Service[Greeter]()

type HelloController struct {
    Greeter *Greeter   // injected by type
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

`curl http://localhost:8080/hello/world` → `{"msg":"hello, world"}`

No constructor, no wiring code. `*Greeter` is a registered type, so when
the framework constructs `*HelloController`, the `Greeter` field is
populated automatically.

---

## Add a database (GORM example)

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

Now any service with a `*gorm.DB` field gets the live DB injected. See
[`database-gorm.md`](./database-gorm.md) for the full pattern.

---

## Add middleware

```go
import "github.com/amberstack/bosun/mw"

func (c *HelloController) Routes(r *bosun.Router) {
    bosun.Get(r, "/:name", c.Hello, bosun.Use[mw.Logging]())
}
```

Or controller-wide:

```go
var _ = bosun.Controller[HelloController]("/hello", bosun.Use[mw.Logging]())
```

Roll your own — see [`middleware.md`](./middleware.md).

---

## Add errors

```go
import "net/http"

func (c *HelloController) Hello(ctx context.Context, req *bosun.Req[HelloIn]) (HelloOut, error) {
    if req.Body.Name == "" {
        return HelloOut{}, bosun.E(http.StatusBadRequest, "name is required", nil)
    }
    return HelloOut{Msg: c.Greeter.Hello(req.Body.Name)}, nil
}
```

`bosun.E(status, publicMsg, cause)` — the client sees `status` + `publicMsg`;
`cause` goes to the audit log only.

---

## What's next

- [`typed-handlers.md`](./typed-handlers.md) — the `Req[In]`/`Out` deep dive.
- [`controllers.md`](./controllers.md) — basic → advanced controllers.
- [`services.md`](./services.md) — DI, lifecycle, hot reload.
- [`middleware.md`](./middleware.md) — including auth + typed context values.
- [`forms.md`](./forms.md), [`files.md`](./files.md) — non-JSON inputs.
- [`database-gorm.md`](./database-gorm.md), [`database-sqlc.md`](./database-sqlc.md) — DB layers.
- [`api-overview.md`](./api-overview.md) — every public symbol with examples.
