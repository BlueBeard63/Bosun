# The bosun CLI

The `bosun` command is the framework's tooling: it serves the documentation, exposes it to AI over MCP, fetches deploy manifests, generates typed clients and registry imports, and scaffolds new monoliths, services and workspaces. This page is the reference for every command and its options.

## Installing

Install the binary with `go install`. Because Bosun is a private module, set `GOPRIVATE` first.

```bash
export GOPRIVATE=github.com/bluebeard63/*
go install github.com/bluebeard63/bosun/cmd/bosun@latest
```

`bosun --version` prints the build version, and `bosun help <command>` prints a command's usage. Every command also accepts `--help`.

## bosun docs

Serves the documentation as a local website and opens it in your browser. The site is embedded in the binary, so it works offline, and it is fully searchable across page titles, headings, and body text.

```bash
bosun docs
```

| Flag | Default | Description |
| --- | --- | --- |
| `--addr` | `127.0.0.1:0` | Listen address as host:port; `:0` picks a free port. |
| `--no-open` | `false` | Do not open a browser automatically; just print the URL. |

## bosun mcp

Serves the documentation to AI clients over the Model Context Protocol on stdin/stdout. It takes no flags, because an MCP client launches it and speaks the protocol over the pipe. See the [MCP page](./mcp.md) for how to install it into Claude, Codex, Cursor, and other tools.

```bash
bosun mcp
```

## bosun manifest

Fetches a running service's deploy manifest from `GET <service-url>/.bosun/manifest` and prints the JSON. The service must opt in to serving it with `manifestmod.Options{Expose: true}`. With `--caddy`, it prints a Caddy reverse-proxy site block derived from the manifest instead. The [manifest guide](./manifest.md) describes the manifest itself.

```bash
bosun manifest http://localhost:8080
bosun manifest --caddy http://localhost:8080
```

| Flag | Default | Description |
| --- | --- | --- |
| `--caddy` | `false` | Emit a Caddyfile instead of the JSON manifest. |

## bosun gen client

Generates a typed Go client from a service's manifest, taking either a URL to a running service or a path to a saved manifest file. Each route becomes a method whose request and response types are the handler's own types. The [client generation guide](./client-gen.md) covers the shared-contracts pattern this relies on.

```bash
bosun gen client http://billing:8080 --package clients --out clients/billing.go
```

| Flag | Default | Description |
| --- | --- | --- |
| `--package` | `client` | Package name for the generated file. |
| `--service` | from manifest | Override the service name used for the client type. |
| `--out`, `-o` | stdout | Write the client to a file instead of printing it. |

## bosun gen registry

Writes `zz_bosun_registry.go`, a file that blank-imports every package in the current module that registers with Bosun (package-level `bosun.Controller`, `Service`, `Middleware`, `Default`, `DefaultBind` or `DefaultDynamic` declarations). Run it from the package that calls `bosun.New()`, usually through `//go:generate bosun gen registry`. The [registration guide](./registration.md) explains what is detected and where the file should live.

```bash
bosun gen registry                    # write ./zz_bosun_registry.go
bosun gen registry --dir ./cmd/worker # write into another main package
bosun gen registry --check            # CI: fail if the file is stale
```

When the module requires bosun v0.8.0 or later, or replaces bosun with a local checkout, the file also registers the doc comments of typed handlers and of struct types and their fields with `bosun.RegisterDocComments`. The OpenAPI spec uses them for summaries and descriptions (see [describe operations and schemas](./openapi.md#describe-operations-and-schemas)). For an older bosun they are left out, since the call doesn't exist there.

| Flag | Default | Description |
| --- | --- | --- |
| `--dir` | `.` | Package directory to write into. |
| `--out`, `-o` | `zz_bosun_registry.go` | Generated file name, relative to `--dir`. |
| `--check` | `false` | Exit non-zero if the file is missing or out of date, without writing it. |
| `--tags` | none | Comma-separated build tags to satisfy, as for `go build -tags`. Files excluded by build constraints are skipped. |
| `--no-docs` | `false` | Leave the doc comments out of the file. |

## bosun new

Scaffolds a new codebase. There are three shapes:

| Command | Produces | Use it for |
| --- | --- | --- |
| `bosun new mono <name>` | One module, one binary, code organised by feature under `internal/<feature>/`. | A single application. |
| `bosun new service <name>` | One service module; inside a workspace, another service in it. | One microservice. |
| `bosun new project <name>` | A Go workspace: a shared `contracts` module and a first service under `services/`. | A microservice architecture. |

Every subcommand prompts for its options interactively, or takes them as flags; pass `--yes` to accept every default without prompting, which is what you want in a script. Pass `--bare` to get only the wiring (`go.mod`, `main.go`, the registry, the `Dockerfile`, and `go.work` and `contracts` for a project) without the demo controller; a bare scaffold still builds and starts, serving the health and manifest endpoints. The [microservices guide](./microservices.md) explains the workspace layout.

### bosun new mono

`bosun new mono <name>` (alias `monolith`) creates a monolith in `./<name>`: a single Go module and binary with no `go.work`, `contracts` module or `services/` split. Code is organised by feature, one package per feature under `internal/<feature>/` holding that feature's controller, service and contract (request and response) types.

```bash
bosun new mono shop --module example.com/shop --yes
```

```text
shop/
  go.mod
  main.go                  # bosun.New() and Run; //go:generate bosun gen registry
  modules.go               # framework modules (health) and the event backend
  zz_bosun_registry.go     # generated: blank-imports every registering package
  Dockerfile
  README.md
  internal/hello/          # demo feature, left out by --bare
    contract.go            # GreetIn, GreetOut
    service.go             # HelloService (bosun.Service)
    controller.go          # HelloController (bosun.Controller), injects *HelloService
```

As with `bosun new service`, the command runs `go mod tidy` afterwards, the name and module path are validated, and it refuses a non-empty directory unless you pass `--force`. Build an image with `docker build .` in the project directory.

| Flag | Default | Description |
| --- | --- | --- |
| `--module` | `example.com/<name>` | Go module path. |
| `--event` | `inmem` | Event backend: `inmem`, `amqp`, `nats`, `redis`, or `none`. |
| `--port` | `8080` | Listen port baked into the app. |
| `--bare` | `false` | Leave out the demo `hello` feature. |
| `--yes`, `-y` | `false` | Accept defaults without prompting. |
| `--force` | `false` | Write into a non-empty directory, overwriting the generated files. |
| `--bosun-dir` | none | As for `bosun new service`. |

### bosun new service

`bosun new service <name>` creates a single deployable service directory. Its `main.go` has a `//go:generate bosun gen registry` directive and a generated `zz_bosun_registry.go`, so new packages are wired in with `go generate` rather than hand-written imports.

The generated `go.mod` requires bosun at the version the CLI was built against, plus the driver module for a broker backend (`amqp`, `nats`, `redis`). The command then runs `go mod tidy` to resolve dependencies and write `go.sum`, so the service builds straight away; if `go` isn't on your `PATH` or tidy fails (for example offline), it prints a warning and you run `go mod tidy` yourself. The `Dockerfile` builds with the `golang` image matching the `go` directive in `go.mod`.

Inside a Go workspace (a `go.work` in the current directory or a parent, such as one made by `bosun new project`), the service is created under `services/<name>` at the workspace root instead of `./<name>`, its module path defaults to `<workspace module>/services/<name>`, it requires the workspace's `contracts` module, and it is added to `go.work` with `go work use`.

The name must start with a letter and contain only letters, digits, `-` and `_`, and the module path must be a valid Go module path. The command refuses to write into a directory that already has files in it unless you pass `--force`.

```bash
bosun new service billing --module example.com/billing --event inmem --yes
```

| Flag | Default | Description |
| --- | --- | --- |
| `--module` | `example.com/<name>`, or `<workspace module>/services/<name>` in a workspace | Go module path. |
| `--event` | `inmem` | Event backend: `inmem`, `amqp`, `nats`, `redis`, or `none`. |
| `--port` | `8080` | Listen port baked into the service. |
| `--bare` | `false` | Leave out the demo `HelloController`; the framework modules and event backend are wired in `modules.go`. |
| `--yes`, `-y` | `false` | Accept defaults without prompting. |
| `--force` | `false` | Write into a non-empty directory, overwriting the generated files. |
| `--bosun-dir` | none | Build against a local bosun checkout by adding `replace` directives to `go.mod`. For working on Bosun itself. |

### bosun new project

`bosun new project <name>` creates a workspace: a `go.work` that uses a shared `contracts` module and a first service under `services/`.

```bash
bosun new project acme --module example.com/acme --service api --yes
```

| Flag | Default | Description |
| --- | --- | --- |
| `--module` | `example.com/<name>` | Go module path for the workspace. |
| `--service` | `api` | Name of the first service. |
| `--event` | `inmem` | Event backend for the first service. |
| `--port` | `8080` | Listen port for the first service. |
| `--bare` | `false` | Create the first service bare (no demo `HelloController`). |
| `--yes`, `-y` | `false` | Accept defaults without prompting. |
| `--force` | `false` | Write into a non-empty directory, overwriting the generated files. |
| `--bosun-dir` | none | As for `bosun new service`. |
