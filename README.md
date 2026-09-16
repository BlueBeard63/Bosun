# Bosun

A zero-ceremony web framework for Go, and the platform toolkit around it. Types self-register where they are declared, and the framework builds the dependency graph, mounts the routes, and generates the OpenAPI spec. On top of that it adds an event queue, distributed tracing, object storage, multi-tenancy, a deploy manifest, and a CLI that serves its own searchable docs.

The core framework is built on `net/http` (Go 1.22+ routing) with no external dependencies. Heavy drivers (RabbitMQ, NATS, Redis, S3, OpenTelemetry, the CLI) live in their own nested modules, so importing core stays light.

## Install

Bosun is a private module, so set `GOPRIVATE` first, then add it to your app.

```sh
go env -w GOPRIVATE=github.com/bluebeard63/*
go get github.com/bluebeard63/bosun
```

Install the CLI the same way:

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
	log.Fatal(bosun.New().Run(":8080"))
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

## CLI commands

| Command | Purpose |
| --- | --- |
| `bosun docs` | Serve the searchable docs and open them in a browser. |
| `bosun mcp` | Serve the docs to AI tools over MCP (stdio). |
| `bosun manifest <url>` | Fetch a service's deploy manifest, or a Caddyfile from it. |
| `bosun gen client <url>` | Generate a typed Go client from a service manifest. |
| `bosun new service\|project <name>` | Scaffold a new service or workspace. |

Run `bosun <command> --help` for options, or see the [CLI reference](./docs/cli.md).

## Repository layout

The core module is `github.com/bluebeard63/bosun` (root package plus `registry/`, `config/`, `mw/`, `audit/`, `openapi/`, and the dependency-light modules under `modules/`). Drivers with heavy dependencies (`modules/eventamqpmod`, `eventnatsmod`, `eventredismod`, `stores3mod`, `traceotelmod`) and the CLI (`cmd/bosun`) are nested modules, tied together for local development by the top-level `go.work`.

## Installing privately

`GOPRIVATE` keeps the module off the public proxy; point Git at your credentials to fetch it.

```sh
go env -w GOPRIVATE=github.com/bluebeard63/*
git config --global url."git@github.com:".insteadOf "https://github.com/"   # or a token in CI
go get github.com/bluebeard63/bosun@vX.Y.Z
```

During development alongside an app, skip publishing with a replace directive in the app's `go.mod` or a `go.work` file:

```
replace github.com/bluebeard63/bosun => ../bosun
```

## Tests

```sh
go test ./...
```

The broker and object-store drivers have integration tests that run only when a server is available, via env vars such as `BOSUN_NATS_URL`, `BOSUN_REDIS_ADDR`, `BOSUN_AMQP_URL`, and `BOSUN_S3_ENDPOINT`; without them those tests skip. Nested driver modules are separate Go modules, so test them from their own directories (or across the workspace).
