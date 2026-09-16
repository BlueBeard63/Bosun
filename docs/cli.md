# The bosun CLI

The `bosun` command is the framework's tooling: it serves the documentation, exposes it to AI over MCP, fetches deploy manifests, generates typed clients, and scaffolds new services. This page is the reference for every command and its options.

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

Fetches a running service's deploy manifest from `GET <service-url>/.bosun/manifest` and prints the JSON. With `--caddy`, it prints a Caddy reverse-proxy site block derived from the manifest instead. The [manifest guide](./manifest.md) describes the manifest itself.

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

## bosun new

Scaffolds a new service or a new workspace. Both subcommands prompt for their options interactively, or take them as flags; pass `--yes` to accept every default without prompting, which is what you want in a script. The [microservices guide](./microservices.md) explains the layout they produce.

`bosun new service <name>` creates a single deployable service directory.

```bash
bosun new service billing --module example.com/billing --event inmem --yes
```

| Flag | Default | Description |
| --- | --- | --- |
| `--module` | `example.com/<name>` | Go module path. |
| `--event` | `inmem` | Event backend: `inmem`, `amqp`, `nats`, `redis`, or `none`. |
| `--port` | `8080` | Listen port baked into the service. |
| `--yes`, `-y` | `false` | Accept defaults without prompting. |

`bosun new project <name>` creates a workspace with a shared contracts package and a first service.

```bash
bosun new project acme --module example.com/acme --service api --yes
```

| Flag | Default | Description |
| --- | --- | --- |
| `--module` | `example.com/<name>` | Go module path for the workspace. |
| `--service` | `api` | Name of the first service. |
| `--event` | `inmem` | Event backend for the first service. |
| `--port` | `8080` | Listen port for the first service. |
| `--yes`, `-y` | `false` | Accept defaults without prompting. |
