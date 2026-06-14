# bosun

A zero-ceremony web framework for Go. No constructors, no wiring files, no
route tables, no codegen — types self-register where they're declared, and
the framework builds the dependency graph, mounts the routes, generates the
OpenAPI spec, and hot-reloads configuration.

Built on `net/http` (Go 1.22+ routing) with zero external dependencies.

## Quickstart

```go
package main

import (
    "context"
    "log"
    "net/http"

    "github.com/amberstack/bosun"
    "github.com/amberstack/bosun/mw"
)

type GreetService struct{}

func (s *GreetService) Hello(name string) string { return "hello, " + name }

var _ = bosun.Service[GreetService]()

type HelloController struct {
    greet *GreetService // injected automatically — even unexported fields
}

var _ = bosun.Controller[HelloController]("/api", bosun.Use[mw.Logging]())

func (c *HelloController) Routes(r *bosun.Router) {
    bosun.Get(r, "/hello/{name}", c.Hello)
}

type HelloRequest struct {
    Name string `path:"name"`
}

type HelloResponse struct {
    Message string `json:"message"`
}

func (c *HelloController) Hello(ctx context.Context, req HelloRequest) (HelloResponse, error) {
    return HelloResponse{Message: c.greet.Hello(req.Name)}, nil
}

func main() {
    log.Fatal(bosun.New().Run(":8080"))
}
```

## Features

**Dependency injection** (`bosun`, `registry`)
- `bosun.Service[T]()` — one line registers a type; fields are injected by
  reflection (unexported included), no constructors needed
- Optional lifecycle hooks: `Init() error` after injection, `Close() error`
  on shutdown (reverse dependency order)
- `registry.RegisterInstance` for external resources (`*gorm.DB`, loggers)
- Dependency graph discovered automatically; cycle detection with readable
  chains; `Validate()` fails fast at boot; Graphviz export via `Reg.DOT()`

**Typed handlers** (`typed.go`)
- `func(ctx, In) (Out, error)` handlers with JSON body binding plus
  `path:"x"` / `query:"x"` tags
- `bosun.E(status, publicMsg, cause)` errors: client sees the public
  message, audit/logs get the cause and the file:line origin

**Middleware** (`mw`)
- Middleware are injectable types: `bosun.Use[mw.RateLimit]()` — typos are
  compile errors, and middleware can have their own injected dependencies
- Built-ins: structured request logging, per-IP rate limiting
  (hot-reloadable limit)

**Modules** (`modules/`)
- A module is a Go package; importing it installs its controllers, routes,
  services, and middleware (`go get` is the package manager)
- Overridable defaults (`bosun.Default`), extension-point interfaces the
  host fulfils (`bosun.DefaultBind`), prefix remapping
  (`bosun.OverridePrefix`), and whole-module disabling (`bosun.Disable`)

**Audit logging** (`audit`)
- Import the package to enable; every typed request emits an event with the
  redacted request/response, status, duration, error, and error origin
- Sensitive fields auto-redacted by name (password, token, secret, ...) or
  explicitly via `audit:"-"` tags
- Bring your own sink: implement `bosun.Auditor` and register it (database,
  SIEM, anything)

**OpenAPI** (`openapi`)
- Import the package to serve `/openapi.json`; schemas reflected from real
  request/response types so the spec cannot drift
- Error responses from three runtime-capable layers: declared
  (`bosun.Errors(409)`), scanned from source (`go/ast`, on disk in dev or
  embedded via `//go:embed` + `openapi.Sources(fs)` in deployed binaries),
  and observed from live traffic

**Hot-reloadable config** (`config`)
- `bosun.Dynamic[T]`: atomic config holders services read per-request, with
  `OnChange` subscriptions
- Layered sources, later wins: `FileSource` (JSON), `EnvSource`
  (environment / .env), `KVSource` (any key-value store — gorm table, Redis,
  etcd — with optional decryption)
- `SecretBox`: AES-256-GCM for config encrypted at rest
- Change detection: bindings only re-apply when bytes actually change

## Layout

```
package bosun (root) — one concern per file:
  doc.go            package documentation
  interfaces.go     MiddlewareHandler, BaseController contracts
  registration.go   Service/Middleware/Controller/Default/DefaultBind, Use
  inject.go         reflective construction + field injection
  app.go            App lifecycle: New, Start, Run, Shutdown
  app_options.go    host options: Disable, OverridePrefix
  router.go         route mounting and middleware chaining
  typed.go          typed handler adapter (bind -> handle -> respond)
  routeinfo.go      route index, RouteOpt, Errors declarations
  binding.go        request binding (JSON body + path/query tags)
  errors.go         bosun.E errors with origin capture
  audit_event.go    AuditEvent, Auditor contract, redaction
  observed.go       runtime status observation
  dynamic.go        hot-reloadable Dynamic[T] config holders

registry/           DI container (usable standalone)
  registry.go       core: nodes, registration, resolution, cycles
  lifecycle.go      Validate (fail-fast boot) + Shutdown ordering
  graph.go          dependency graph export (adjacency + Graphviz DOT)
  ref.go            lazy Ref[T] handles + struct injection
  runtime.go        reflect.Type-based API for framework internals

mw/                 built-in middleware (logging.go, ratelimit.go)
audit/              slog audit sink module
openapi/            controller.go, schema.go, errscan.go, sources.go
config/             source.go, bind.go, watcher.go, file.go, env.go,
                    kv.go, crypto.go (SecretBox)
modules/            example installable feature modules
examples/
  basic/            services, middleware, controllers
  modules/          installing modules, extension points
  repo/             generic Repo[T] backed by GORM + SQLite
  kitchen-sink/     everything: typed handlers, audit, OpenAPI,
                    encrypted hot-reload config
```

## Running the examples

```sh
go run ./examples/basic            # :8090
go run ./examples/modules          # :8091
go run ./examples/repo             # :8090 — Repo[T] CRUD over SQLite
cd examples/kitchen-sink && CONFIG_MASTER_KEY=dev-key go run .   # :8094
```

Kitchen-sink endpoints to try:

```sh
curl -X POST localhost:8094/auth/login -H 'Content-Type: application/json' \
     -d '{"email":"jack@amberstack.dev","password":"hunter2"}'
curl localhost:8094/openapi.json
curl -X POST localhost:8094/admin/ratelimit -H 'Content-Type: application/json' \
     -d '{"per_minute":50}'        # encrypted config write, applies live
```

## Installing privately (closed source)

bosun stays private by hosting it in a private Git repository and telling the
Go toolchain not to use the public proxy for it. No special server needed.

**1. Push to a private repo** (GitHub org, or self-hosted Gitea/GitLab):

```sh
cd bosun-release
git init && git add -A && git commit -m "bosun v0.1.0"
git remote add origin git@github.com:amberstack/bosun.git   # private repo
git push -u origin main
git tag v0.1.0 && git push --tags
```

**2. On every machine that consumes it** (devs and CI):

```sh
go env -w GOPRIVATE=github.com/amberstack/*
git config --global url."git@github.com:".insteadOf "https://github.com/"
```

GOPRIVATE makes `go get` skip proxy.golang.org and sum.golang.org for your
org (so nothing leaks publicly), and the git rewrite makes fetches use your
SSH key. In CI, use a token instead of SSH:

```sh
git config --global url."https://x-access-token:${GH_TOKEN}@github.com/".insteadOf "https://github.com/"
```

**3. Consume it like any module:**

```sh
go get github.com/amberstack/bosun@v0.1.0
```

**During development** of bosun alongside an app, skip publishing entirely
with a replace directive in the app's go.mod (or a go.work file):

```
replace github.com/amberstack/bosun => ../bosun
```

**For air-gapped or client-shipped builds**, vendor it: `go mod vendor`
commits a full copy into the consuming repo, removing the network and the
private-auth requirement from builds entirely.

## Notes

- Rename the module: change the path in `go.mod` and run
  `grep -rl github.com/amberstack/bosun . | xargs sed -i 's|github.com/amberstack/bosun|your/path|g'`
- One app per process: registrations are package-level (same model as
  `database/sql` drivers)
- Structure (routes, services, middleware) is immutable after `Start()` by
  design — that's what makes the post-boot registry lock-free; config
  *values* hot-reload via `Dynamic[T]`
- The rate limiter is per-process; swap in a shared-store implementation
  behind the same `Handle` interface for multi-node deployments
- Tests: `go test ./...`
