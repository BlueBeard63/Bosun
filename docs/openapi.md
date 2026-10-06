# OpenAPI

Bosun generates an OpenAPI 3.0 spec from your typed handlers and serves it at `GET /openapi.json`. There are no annotations or separate schema files: route paths, parameters, request bodies and response schemas come from your Go types, and error responses come from three sources that you control. This page is a set of how-to guides for common tasks. For a guided first run, follow the [OpenAPI tutorial](./openapi-tutorial.md). For exact mappings and limits, see the [OpenAPI reference](./openapi-reference.md).

<figure class="diagram">
<svg viewBox="0 0 620 250" role="img" aria-labelledby="oa-title oa-desc" xmlns="http://www.w3.org/2000/svg">
<title id="oa-title">How the OpenAPI spec is built</title>
<desc id="oa-desc">The app's typed routes give paths, parameters and schemas. Three error layers, declared, scanned and observed, add error responses. The spec controller merges them on each request to /openapi.json.</desc>
<defs>
<marker id="oa-arw" markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto"><polygon points="0 0, 8 3, 0 6" fill="var(--fg-muted)"/></marker>
</defs>
<rect x="20" y="20" width="190" height="56" rx="8" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="115" y="44" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">app.TypedRoutes()</text>
<text x="115" y="62" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">paths · params · schemas</text>
<rect x="20" y="100" width="190" height="40" rx="8" fill="var(--code-bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="115" y="124" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="10" fill="var(--fg)">declared: bosun.Errors(...)</text>
<rect x="20" y="148" width="190" height="40" rx="8" fill="var(--code-bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="115" y="172" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="10" fill="var(--fg)">scanned: bosun.E(...) in source</text>
<rect x="20" y="196" width="190" height="40" rx="8" fill="var(--code-bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="115" y="220" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="10" fill="var(--fg)">observed: real responses ≥ 400</text>
<text x="226" y="96" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="8" letter-spacing="0.12em" fill="var(--fg-muted)">ERROR LAYERS</text>
<line x1="210" y1="48" x2="318" y2="118" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#oa-arw)"/>
<line x1="210" y1="120" x2="318" y2="124" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#oa-arw)"/>
<line x1="210" y1="168" x2="318" y2="130" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#oa-arw)"/>
<line x1="210" y1="216" x2="318" y2="136" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#oa-arw)"/>
<rect x="322" y="96" width="132" height="64" rx="8" fill="var(--accent-soft)" stroke="var(--accent)" stroke-width="1"/>
<text x="388" y="124" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--accent)">SpecController</text>
<text x="388" y="142" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--accent)">merges per request</text>
<line x1="454" y1="128" x2="486" y2="128" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#oa-arw)"/>
<rect x="490" y="104" width="112" height="48" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="546" y="132" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="10" fill="var(--fg)">/openapi.json</text>
</svg>
<figcaption>Routes and schemas come from the app's typed handlers. Error responses are merged from three layers every time the spec is requested.</figcaption>
</figure>

## Enable the spec

Import the package for its side effects, anywhere in your binary:

```go
import _ "github.com/bluebeard63/bosun/openapi"
```

The package registers `openapi.SpecController`, which mounts `GET /openapi.json` when the app starts. The spec is built fresh on every request from the typed routes of the app that serves it (`app.TypedRoutes()`), so it always matches the running code. Raw routes registered with `r.Get` and similar methods are not included.

## Serve the spec at another path

Remount the controller with `OverridePrefix`:

```go
app := bosun.New(bosun.OverridePrefix[openapi.SpecController]("/api"))
// spec now at GET /api/openapi.json; /openapi.json returns 404
```

## Turn the spec off

Disable the package for one app without removing the import:

```go
app := bosun.New(bosun.Disable("github.com/bluebeard63/bosun/openapi"))
```

To leave the package out of a binary completely, put the import in a file with a build tag, for example `openapi_dev.go`:

```go
//go:build !prod

package main

import _ "github.com/bluebeard63/bosun/openapi"
```

Builds made with `go build -tags prod` then contain no spec endpoint and no scanner.

## Restrict who can read the spec

`SpecController` is declared without middleware. To protect it, guard its path with app-wide middleware:

```go
func internalOnly(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/openapi.json" && r.Header.Get("X-Internal-Token") != os.Getenv("INTERNAL_TOKEN") {
            http.NotFound(w, r)
            return
        }
        next.ServeHTTP(w, r)
    })
}

app := bosun.New(bosun.WithMiddleware(bosun.UseFunc(internalOnly)))
```

Alternatively, remount it under a prefix that your gateway only exposes internally. See [app-wide middleware](./middleware.md#app-wide-middleware).

## Document error responses

Every operation always lists `200` and `500`, and lists `400` when the handler binds input. Other error statuses come from three layers, which are merged together:

| Layer | How | When it works |
|---|---|---|
| Declared | `bosun.Errors(codes...)` on the route | Always, including deployed binaries |
| Scanned | `bosun.E(status, ...)` written directly in the handler method | When source is on disk or embedded |
| Observed | Any status `>= 400` the route actually returned | After traffic, until the process restarts |

### Declare statuses on the route

Use declarations for any status the scanner can't see: errors built in helper functions or services, statuses held in variables, and errors passed up from other packages.

```go
bosun.Post(r, "/", c.Create, bosun.Errors(http.StatusConflict, http.StatusGone))
```

Declared statuses also appear in the [deploy manifest](./manifest.md).

### Let the scanner find `bosun.E` calls

You don't need to do anything for this. At the first request for the spec, the scanner parses the `.go` files under the working directory (skipping `_test.go`, `vendor`, `node_modules` and hidden directories). It records the status of each `bosun.E(status, ...)` call found in a method body, and attaches those statuses to the route whose handler is that method:

```go
func (c *NotesController) Get(ctx context.Context, req *bosun.Req[GetNoteIn]) (NoteOut, error) {
    // ...
    return NoteOut{}, bosun.E(http.StatusNotFound, "note not found", nil) // → 404 in the spec
}
```

For the scanner to see a status, the status must be an integer literal (`404`) or an `http.Status...` constant, and the call must be written in the handler method itself. The [reference](./openapi-reference.md#source-scanning-rules) has the full rules.

### Scan a different source directory

The scanner starts from `ScanOptions.SourceDir`, which defaults to `"."`, the process's working directory. If you run the binary from somewhere else, point it at your source:

```go
app := bosun.New()
registry.RegisterInstance[*openapi.ScanOptions](app.Reg, &openapi.ScanOptions{SourceDir: "./internal"})
```

An empty `SourceDir` turns off on-disk scanning.

### Scan source in deployed binaries

A deployed binary usually runs without its source code nearby, so on-disk scanning finds nothing. Embed the handler source and register it in each package that contains handlers:

```go
package notes

import (
    "embed"

    "github.com/bluebeard63/bosun/openapi"
)

//go:embed *.go
var handlerSource embed.FS

var _ = openapi.Sources(handlerSource)
```

This puts the package's source code inside the binary, where anyone with the binary can read it. For closed-source services, use `bosun.Errors` declarations instead.

### Rely on observed statuses

Every typed response records its status against its route. Any status `>= 400` that a route has actually returned appears in that app's spec, described with its standard text (for example, `507 Insufficient Storage`). This catches statuses computed at runtime, but observed statuses are kept in memory only. Declare any status that clients must always see.

## Shape the schemas

Schemas are generated from your `In` and `Out` types:

- **Field names** follow `json` tags. Fields tagged `json:"-"` keep their Go name (see [limitations](./openapi-reference.md#limitations)), and unexported fields are skipped.
- **Named structs** become reusable entries under `components/schemas`, referenced with `$ref`. Anonymous structs are written inline.
- **`path:"..."` fields** become path parameters, and **`query:"..."` fields** become optional query parameters. Neither appears in the body schema.
- **Request bodies** are documented for `POST`, `PUT` and `PATCH` when the `In` struct has at least one body field.

Give request and response types distinct, descriptive names. Schemas are keyed by the bare type name, so two different `User` types in two packages would share one schema entry.

## Add an interactive documentation page

Serve Swagger UI (or Redoc) from a raw route and point it at the spec. The [tutorial](./openapi-tutorial.md#step-8-browse-the-api-in-swagger-ui) has a complete `DocsController` you can copy. Because it uses a raw route, the page itself stays out of the spec.

## Export the spec in CI

To commit the spec or check it in CI, write it from a Go test. No server or network is needed:

```go
// openapi_export_test.go
func TestExportOpenAPI(t *testing.T) {
    app := bosun.New()
    if err := app.Start(); err != nil {
        t.Fatal(err)
    }
    t.Cleanup(func() { _ = app.Shutdown() })

    rec := httptest.NewRecorder()
    app.ServeHTTP(rec, httptest.NewRequest("GET", "/openapi.json", nil))
    if rec.Code != http.StatusOK {
        t.Fatalf("GET /openapi.json: %d", rec.Code)
    }
    if err := os.WriteFile("openapi.json", rec.Body.Bytes(), 0o644); err != nil {
        t.Fatal(err)
    }
}
```

Put the test in your `main` package (or whichever package imports your controllers) so every route is registered. In CI, fail the build when the committed spec is out of date:

```sh
go test -run TestExportOpenAPI . && git diff --exit-code openapi.json
```

The test runs in the package directory, which is also where the scanner looks for source, so scanned statuses are included.

## Work with several apps and in tests

The spec describes only the app that serves it. Two apps in one process, for example in parallel tests or with different `OverridePrefix` options, each produce their own spec, and traffic on one never adds observed statuses to the other. To inspect routes without going through HTTP, call `app.TypedRoutes()`. See [routing internals](./routing-internals.md#inspecting-registered-routes).
