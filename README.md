# Bosun

A zero-ceremony web framework for Go, and the platform toolkit around it. Types self-register where they are declared, and the framework builds the dependency graph, mounts the routes, and generates the OpenAPI spec. On top of that it adds an event queue, distributed tracing, object storage, multi-tenancy, a deploy manifest, and a CLI that serves its own searchable docs.

The core framework is built on `net/http` (Go 1.22+ routing), and its packages import only the standard library. The root module's `go.mod` does require GORM and its SQLite driver: only `modules/gormrepomod` and `modules/outboxmod` import GORM, and SQLite (cgo, via `mattn/go-sqlite3`) is used only by tests and `examples/repo`, so neither is compiled into your app unless you import those packages. Drivers with heavy dependencies (RabbitMQ, NATS, Redis, S3, OpenTelemetry) and the CLI live in their own nested modules, so importing core stays light.

## Install

Add the framework to your app's module:

```sh
go get github.com/bluebeard63/bosun
```

Install the CLI:

```sh
go install github.com/bluebeard63/bosun/cmd/bosun@latest
```

## Quickstart

```go
package main

import (
	"context"
	"log"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/mw"
)

type GreetService struct{}

func (s *GreetService) Hello(name string) string { return "hello, " + name }

var _ = bosun.Service[GreetService]()

type HelloController struct {
	Greet *GreetService // injected automatically
}

var _ = bosun.Controller[HelloController]("/api", bosun.Use[mw.Logging]())

func (c *HelloController) Routes(r *bosun.Router) {
	bosun.Get(r, "/hello/{name}", c.Hello)
}

type HelloIn struct {
	Name string `path:"name"`
}

type HelloOut struct {
	Message string `json:"message"`
}

func (c *HelloController) Hello(ctx context.Context, req *bosun.Req[HelloIn]) (HelloOut, error) {
	return HelloOut{Message: c.Greet.Hello(req.Body.Name)}, nil
}

func main() {
	if err := bosun.New().Run(":8080"); err != nil {
		log.Fatal(err)
	}
}
```

```sh
go run .
curl localhost:8080/api/hello/world   # {"message":"hello, world"}
```

## Read the docs

The full documentation ships inside the CLI. Serve it as a searchable website that opens in your browser:

```sh
bosun docs
```

Or expose it to an AI coding tool (Claude, Codex, Cursor, and others) over MCP:

```sh
claude mcp add bosun-docs -- bosun mcp
```

The pages also live as plain Markdown under [`docs/`](./docs); start at [`docs/getting-started.md`](./docs/getting-started.md).

## What's included

- **Core**: dependency injection, typed handlers, middleware, errors, hot-reloadable config, audit logging, OpenAPI. See the [core docs](./docs/README.md).
- **Data**: a generic `Repo[T]` with a query builder and transactions, plus GORM and sqlc guides.
- **Events**: publish/subscribe, work queues, topics, and RPC over an in-memory bus or **RabbitMQ, NATS, or Redis Streams**. See [events](./docs/events.md).
- **Platform**: correlation IDs and OpenTelemetry [tracing](./docs/tracing.md), health readiness probes, [object storage](./docs/storage.md) (local, S3, WebDAV), [Infisical secrets](./docs/secrets-infisical.md), [multi-tenancy](./docs/multitenancy.md), [webhooks](./docs/webhooks.md) and a [transactional outbox](./docs/outbox.md), and a [deploy manifest](./docs/manifest.md) with Caddyfile generation.
- **Tooling**: the [`bosun` CLI](./docs/cli.md) serves docs, exposes them over [MCP](./docs/mcp.md), fetches manifests, [generates typed clients](./docs/client-gen.md), and [scaffolds services](./docs/microservices.md).

> **`modules/authmod` is a demo, not authentication.** It logs a user in with an email and no credential, and its `/auth/check/{email}` route lets anyone enumerate users. It exists to show how a module is built; do not import it into a production service. See [auth and permissions](./docs/auth-and-permissions.md) for real authentication.

## CLI commands

| Command | Purpose |
| --- | --- |
| `bosun docs` | Serve the searchable docs and open them in a browser. |
| `bosun mcp` | Serve the docs to AI tools over MCP (stdio). |
| `bosun manifest <url>` | Fetch a service's deploy manifest, or a Caddyfile from it. The service must opt in with `manifestmod.Options.Expose`. |
| `bosun gen client <url-or-file>` | Generate a typed Go client from a service manifest. |
| `bosun gen registry` | Generate the file that blank-imports every package that registers with Bosun (usually run via `//go:generate`). |
| `bosun new mono <name>` | Scaffold a monolith: one module and binary, organised by feature (alias `monolith`). |
| `bosun new service\|project <name>` | Scaffold a single service, or a microservice workspace with a shared contracts module. |
| `bosun add controller\|service\|middleware <name>` | Add a controller, service or middleware to an existing module, then regenerate its registry file. |

The `bosun new` commands run `go mod tidy`, so the result builds straight away, and `--bare` writes only the wiring, without the demo controller. The `bosun add` commands take `--dir` and `--pkg` to choose the package, `--in <service>` to target a service in a workspace, and `--force` to overwrite an existing file; `bosun add controller` also takes `--prefix` for its route prefix.

Run `bosun <command> --help` for options, or see the [CLI reference](./docs/cli.md).

## Repository layout

The core module is `github.com/bluebeard63/bosun` (root package plus `registry/`, `config/`, `mw/`, `audit/`, `openapi/`, `bosuntest/`, and the modules under `modules/` not listed below). Drivers with heavy dependencies (`modules/eventamqpmod`, `eventnatsmod`, `eventredismod`, `stores3mod`, `traceotelmod`) and the CLI (`cmd/bosun`) are nested modules, released with their own tags. `modules/outboxmod/pgtest` is a test-only nested module that keeps the Postgres driver out of the root module; it is not released.

Each module declares its minimum Go version in its own `go.mod`:

| Module | Minimum Go |
| --- | --- |
| `github.com/bluebeard63/bosun` (core) | 1.22 |
| `cmd/bosun` (CLI) | 1.22 |
| `modules/eventamqpmod`, `modules/eventredismod` | 1.22 |
| `modules/eventnatsmod`, `modules/stores3mod`, `modules/traceotelmod` | 1.25 |

The nested modules require the root module by its released version. The repository does not commit a `go.work`; to build and test them against your local checkout, create the same workspace CI does, at the repository root:

```sh
go work init . ./cmd/bosun ./modules/eventamqpmod ./modules/eventnatsmod \
  ./modules/eventredismod ./modules/stores3mod ./modules/traceotelmod
```

The workspace needs Go 1.25, the highest minimum among its modules. See [CONTRIBUTING.md](./CONTRIBUTING.md) for the checks CI runs.

## Developing against a local checkout

To use an unreleased Bosun from an app, point the app at your checkout with a replace directive in the app's `go.mod` (or with a `go.work` file):

```
replace github.com/bluebeard63/bosun => ../bosun
```

## Tests

```sh
go test ./...
```

The GORM tests use SQLite, which needs cgo (a C compiler on your `PATH`). The broker and object-store drivers have integration tests that run only when a server is available, via env vars such as `BOSUN_NATS_URL`, `BOSUN_REDIS_ADDR`, `BOSUN_AMQP_URL`, and `BOSUN_S3_ENDPOINT`; without them those tests skip. Nested driver modules are separate Go modules, so test them from their own directories inside the workspace described under [Repository layout](#repository-layout).
