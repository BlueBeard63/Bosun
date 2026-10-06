# Tutorial: document an API with OpenAPI

In this tutorial you build a small notes API and add a live OpenAPI 3.0 spec to it, so the API documents itself. Along the way you see how Bosun turns typed handlers into schemas, how it discovers error responses, and how to browse the result in Swagger UI. It takes about fifteen minutes.

By the end you will have:

- a running API with three typed routes;
- an OpenAPI spec at `GET /openapi.json` that lists every route, parameter, request body, response schema and error status;
- an interactive Swagger UI page at `GET /docs`;
- the spec saved to a file you can commit or give to a client generator.

You need Go 1.22 or newer and `curl`. If you haven't built a Bosun service before, read [getting started](./getting-started.md) first; this tutorial assumes you know what a controller and a typed handler are.

## Step 1: create the project

Make a new module and add Bosun:

```sh
mkdir notes && cd notes
go mod init example.com/notes
go get github.com/bluebeard63/bosun
```

## Step 2: write the notes API

Create `main.go` with a controller that lists, fetches and creates notes. The notes live in memory so you can focus on the API itself.

```go
package main

import (
    "context"
    "log"
    "net/http"
    "sync"
    "time"

    "github.com/bluebeard63/bosun"
)

type NoteIn struct {
    Title string `json:"title"`
    Body  string `json:"body"`
}

type NoteOut struct {
    ID        int       `json:"id"`
    Title     string    `json:"title"`
    Body      string    `json:"body"`
    CreatedAt time.Time `json:"created_at"`
}

type GetNoteIn struct {
    ID int `path:"id"`
}

type ListNotesIn struct {
    Limit int `query:"limit"`
}

type NotesController struct {
    mu    sync.Mutex
    notes []NoteOut
}

var _ = bosun.Controller[NotesController]("/notes")

func (c *NotesController) Routes(r *bosun.Router) {
    bosun.Get(r, "/", c.List)
    bosun.Get(r, "/{id}", c.Get)
    bosun.Post(r, "/", c.Create)
}

func (c *NotesController) List(ctx context.Context, req *bosun.Req[ListNotesIn]) ([]NoteOut, error) {
    c.mu.Lock()
    defer c.mu.Unlock()
    out := c.notes
    if n := req.Body.Limit; n > 0 && n < len(out) {
        out = out[:n]
    }
    return out, nil
}

func (c *NotesController) Get(ctx context.Context, req *bosun.Req[GetNoteIn]) (NoteOut, error) {
    c.mu.Lock()
    defer c.mu.Unlock()
    for _, n := range c.notes {
        if n.ID == req.Body.ID {
            return n, nil
        }
    }
    return NoteOut{}, bosun.E(http.StatusNotFound, "note not found", nil)
}

func (c *NotesController) Create(ctx context.Context, req *bosun.Req[NoteIn]) (NoteOut, error) {
    c.mu.Lock()
    defer c.mu.Unlock()
    n := NoteOut{ID: len(c.notes) + 1, Title: req.Body.Title, Body: req.Body.Body, CreatedAt: time.Now()}
    c.notes = append(c.notes, n)
    return n, nil
}

func main() {
    if err := bosun.New().Run(":8080"); err != nil {
        log.Fatal(err)
    }
}
```

Run it and create a note to check that it works:

```sh
go run .
```

```sh
curl -H 'Content-Type: application/json' -d '{"title":"Groceries","body":"milk"}' localhost:8080/notes
```

You see the new note:

```json
{"id":1,"title":"Groceries","body":"milk","created_at":"2026-10-06T12:24:26.59+01:00"}
```

Notice that `bosun.Post(r, "/", ...)` under the `/notes` prefix is served at `/notes`, without a trailing slash.

## Step 3: turn on OpenAPI

Add one blank import to `main.go`:

```go
import (
    // ...
    "github.com/bluebeard63/bosun"
    _ "github.com/bluebeard63/bosun/openapi" // serves GET /openapi.json
)
```

That is the whole setup. The `openapi` package registers its own controller, and it is mounted when the app starts, like any other controller.

Stop the server with Ctrl+C, start it again, and fetch the spec:

```sh
go run .
```

```sh
curl localhost:8080/openapi.json
```

You get an OpenAPI 3.0.3 document with a `paths` entry for each route and a `components.schemas` entry for each named type.

## Step 4: read what Bosun generated

Look at the operation for `GET /notes/{id}`. Shortened, it reads:

```json
"/notes/{id}": {
  "get": {
    "operationId": "Get",
    "parameters": [
      { "name": "id", "in": "path", "required": true, "schema": { "type": "integer" } }
    ],
    "responses": {
      "200": { "description": "OK", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/NoteOut" } } } },
      "400": { "description": "invalid request body or parameters", "...": "..." },
      "404": { "description": "Not Found", "...": "..." },
      "500": { "description": "internal server error", "...": "..." }
    }
  }
}
```

Each part comes from your Go code:

- **The path and method** come from `bosun.Get(r, "/{id}", ...)` and the controller prefix.
- **The `id` path parameter** comes from the `{id}` segment. It is typed `integer` because `GetNoteIn.ID` is an `int` tagged `path:"id"`.
- **The `200` response** points at `NoteOut`, the handler's `Out` type. The `NoteOut` schema uses your `json` tag names, and `time.Time` becomes a `date-time` string.
- **`400` and `500`** are added automatically: `400` because the handler binds input that can be invalid (its body lists the invalid fields), and `500` because any handler can fail unexpectedly.
- **`404`** was found by reading your source code. The next step explains how.

Now look at the other two operations:

- `GET /notes` has a `limit` query parameter, taken from the `query:"limit"` tag on `ListNotesIn`.
- `POST /notes` has a required JSON `requestBody` that points at the `NoteIn` schema.

## Step 5: see how error responses are found

`Get` returns `bosun.E(http.StatusNotFound, ...)`. At startup, the `openapi` package parses the Go files in the working directory, finds `bosun.E` calls inside each handler method, and adds their statuses to that route. That's where the `404` came from, without you declaring anything.

Try it. Add a rule to `Create` that rejects duplicate titles, using a small helper to build the error:

```go
func (c *NotesController) Create(ctx context.Context, req *bosun.Req[NoteIn]) (NoteOut, error) {
    c.mu.Lock()
    defer c.mu.Unlock()
    for _, n := range c.notes {
        if n.Title == req.Body.Title {
            return NoteOut{}, duplicateTitle(n.Title)
        }
    }
    n := NoteOut{ID: len(c.notes) + 1, Title: req.Body.Title, Body: req.Body.Body, CreatedAt: time.Now()}
    c.notes = append(c.notes, n)
    return n, nil
}

func duplicateTitle(title string) error {
    return bosun.E(http.StatusConflict, "a note titled "+title+" already exists", nil)
}
```

Restart the server and look at the responses for `POST /notes`:

```sh
curl -s localhost:8080/openapi.json | grep -A12 '"post"'
```

There is no `409`. The scanner only looks inside the handler method itself, and this `bosun.E` call is in a separate helper function, so the spec can't link it to `Create`.

## Step 6: declare the status yourself

When the scanner can't see an error, declare it on the route with `bosun.Errors`:

```go
bosun.Post(r, "/", c.Create, bosun.Errors(http.StatusConflict))
```

Restart the server and fetch the spec again. `POST /notes` now lists `409` alongside `200`, `400` and `500`. Declarations always appear, even in a deployed binary that has no source code to scan.

Confirm the API really returns it:

```sh
curl -H 'Content-Type: application/json' -d '{"title":"Groceries"}' localhost:8080/notes
curl -H 'Content-Type: application/json' -d '{"title":"Groceries"}' localhost:8080/notes
```

The second call answers `{"error":"a note titled Groceries already exists"}` with status `409`.

## Step 7: watch the spec learn from traffic

Some statuses are only known at runtime. Make the notebook hold at most three notes, and compute the status in a function:

```go
    if len(c.notes) >= 3 {
        return NoteOut{}, bosun.E(statusFull(), "notebook full", nil)
    }
```

Put that check in `Create`, just before the new note is built, and add the function:

```go
func statusFull() int { return http.StatusInsufficientStorage }
```

Restart the server. The spec for `POST /notes` has no `507` yet, because the scanner only understands literal numbers and `http.Status...` constants, not function calls. Now send four notes:

```sh
for t in Groceries Work Ideas Travel; do
  curl -s -w ' %{http_code}\n' -H 'Content-Type: application/json' -d "{\"title\":\"$t\"}" localhost:8080/notes
done
```

The fourth answers `507`. Fetch the spec again: `POST /notes` now lists `507 Insufficient Storage`. Bosun records every error status (`>= 400`) a route actually returns and adds it to the spec. These observed statuses are kept in memory, so they reset when the process restarts.

## Step 8: browse the API in Swagger UI

The JSON spec is meant for tools. For people, serve Swagger UI. Add a second controller to `main.go` that returns a small HTML page, which loads Swagger UI from a CDN and points it at your spec:

```go
type DocsController struct{}

var _ = bosun.Controller[DocsController]("")

func (c *DocsController) Routes(r *bosun.Router) {
    r.Get("/docs", c.ui) // raw route: not part of the spec itself
}

func (c *DocsController) ui(w http.ResponseWriter, _ *http.Request) {
    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    _, _ = w.Write([]byte(swaggerUI))
}

const swaggerUI = `<!doctype html>
<html>
<head>
  <title>Notes API</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>SwaggerUIBundle({ url: "/openapi.json", dom_id: "#swagger-ui" });</script>
</body>
</html>`
```

`/docs` is registered with the raw `r.Get`, so it doesn't appear in the spec. Raw routes are never included.

Restart the server and open <http://localhost:8080/docs> in a browser. You see the three notes operations with their schemas and error responses. Expand `POST /notes`, choose **Try it out**, and send a request straight from the page.

## Step 9: save the spec to a file

A spec file is useful in code review, CI and client generators. With the server running:

```sh
curl -s localhost:8080/openapi.json -o openapi.json
```

`openapi.json` is a standard OpenAPI 3.0.3 document. You can give it to tools such as `oapi-codegen` or `openapi-generator` to produce clients in other languages. For Go-to-Go calls between Bosun services, [`bosun gen client`](./client-gen.md) generates a typed client from the deploy manifest instead.

## What you learned

You turned a plain Bosun API into a self-documenting one with a single import, and you used all three sources of error responses:

- **scanned:** `bosun.E` calls inside handler methods (`404`);
- **declared:** `bosun.Errors(...)` on the route (`409`);
- **observed:** statuses the route actually returned (`507`).

Next steps:

- The [OpenAPI how-to guides](./openapi.md) cover moving or hiding the spec, scanning source in deployed binaries, exporting the spec in CI, and working with several apps.
- The [OpenAPI reference](./openapi-reference.md) lists exactly how Go types and routes map to the spec, and the current limitations.
