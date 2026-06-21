# Controllers

A controller is a Go struct that owns a group of related routes and the
services those routes need. Bosun discovers controllers via a single
package-level declaration; no wiring code in `main`.

---

## Basics

### 1. Declare the controller

```go
type UsersController struct{}

var _ = bosun.Controller[UsersController]("/users")
```

`bosun.Controller[T]("/prefix")` registers `T` and mounts its routes under
`/prefix`. The `var _ =` line just runs the registration at package init —
the returned `struct{}` is meaningless.

### 2. Implement `Routes(*bosun.Router)`

```go
func (c *UsersController) Routes(r *bosun.Router) {
    bosun.Get(r,  "/:id", c.Get)
    bosun.Post(r, "/",    c.Create)
}
```

Paths are joined onto the controller prefix. The example above mounts:
- `GET  /users/:id`
- `POST /users/`

### 3. Write handlers

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

### Full minimal example

```go
package main

import (
    "context"
    "log"
    "net/http"

    "github.com/amberstack/bosun"
)

type UsersController struct{}
var _ = bosun.Controller[UsersController]("/users")

type GetIn  struct{ ID int    `path:"id"` }
type UserOut struct{ ID int   `json:"id"`; Name string `json:"name"` }

func (c *UsersController) Routes(r *bosun.Router) {
    bosun.Get(r, "/:id", c.Get)
}

func (c *UsersController) Get(ctx context.Context, req *bosun.Req[GetIn]) (UserOut, error) {
    if req.Body.ID == 0 {
        return UserOut{}, bosun.E(http.StatusBadRequest, "id required", nil)
    }
    return UserOut{ID: req.Body.ID, Name: "Jack"}, nil
}

func main() { log.Fatal(bosun.New().Run(":8080")) }
```

---

## Injecting services

Any field on the controller whose type is registered via `bosun.Service[T]`
(or `registry.RegisterInstance[T]`) gets injected automatically — exported
or unexported, no constructor needed.

```go
type UsersController struct {
    Users *UserService   // injected
    log   *slog.Logger   // injected (registered as instance)
}

var _ = bosun.Controller[UsersController]("/users")
```

To skip injection on a field, tag it `inject:"-"`:

```go
type UsersController struct {
    Users *UserService
    Cache map[int]string `inject:"-"`   // left zero-valued
}
```

Plain (unregistered) types are always left zero — no error.

See [`services.md`](./services.md) for how to register dependencies.

---

## Controller-wide middleware

Pass `bosun.Use[T]()` after the prefix; the middleware runs on every route
the controller mounts.

```go
var _ = bosun.Controller[Admin]("/admin",
    bosun.Use[mw.Logging](),
    bosun.Use[mw.RequireStaff](),
)
```

Per-route middleware appends as additional arguments to `bosun.Get/Post/...`:

```go
bosun.Post(r, "/login", c.Login, bosun.Use[mw.RateLimit]())
```

See [`middleware.md`](./middleware.md).

---

## Multiple controllers

Each controller is one struct, declared in its own file. The framework picks
them all up at `bosun.New()`. There is no central registry to maintain.

```go
// users/controller.go
var _ = bosun.Controller[UsersController]("/users")

// orgs/controller.go
var _ = bosun.Controller[OrgsController]("/orgs")
```

In `main.go`, just import the packages (often via a blank import if the
controller has no other public types you need):

```go
import (
    _ "myapp/users"
    _ "myapp/orgs"
)
```

---

## Sub-groups

For controllers that mount several related routes sharing a sub-prefix or
middleware, `r.Group(prefix, mws...)` returns a child router. Routes
mounted on the child get the combined prefix and the inherited
middleware stack plus whatever the group adds.

```go
func (c *Admin) Routes(r *bosun.Router) {
    bosun.Get(r, "/ping", c.Ping)   // /admin/ping

    staff := r.Group("/staff", bosun.Use[mw.RequireStaff]())
    bosun.Get(staff,  "/users", c.ListUsers)  // /admin/staff/users + RequireStaff
    bosun.Post(staff, "/wipe",  c.Wipe)       // /admin/staff/wipe  + RequireStaff

    // Groups nest. Sub-group routes get all parents' middleware.
    v2 := staff.Group("/v2")
    bosun.Get(v2, "/metrics", c.Metrics)      // /admin/staff/v2/metrics + RequireStaff
}
```

Both typed (`bosun.Get(staff, ...)`) and raw (`staff.Get(...)`) handlers
work on a group.

### Middleware-only groups

Pass an empty prefix to apply middleware to a batch of routes without
adding a path segment:

```go
guarded := r.Group("", bosun.Use[mw.RequireAuth]())
bosun.Get(guarded,  "/profile",  c.Profile)
bosun.Post(guarded, "/settings", c.UpdateSettings)
```

### Path syntax

Sub-group prefixes accept the same `:name` / `{name}` forms as routes:

```go
items := r.Group("/items/:id")
bosun.Get(items, "/show", c.Show)   // /things/items/{id}/show, with path:"id"
```

---

## Mounting at runtime

### Override the prefix from the host

```go
app := bosun.New(
    bosun.OverridePrefix[users.UsersController]("/v2/users"),
)
```

Useful for versioning a controller you import from a module.

### Disable a whole package

```go
app := bosun.New(
    bosun.Disable("github.com/amberstack/bosun/audit"),
)
```

Every `Service` / `Controller` / `Default` declared in that import path is
skipped. Good for uninstalling optional modules.

---

## Mixing typed and raw handlers

`*bosun.Router` has two parallel APIs:

```go
func (c *Files) Routes(r *bosun.Router) {
    bosun.Get(r, "/files/:id", c.Get)    // typed: parsed body + audit + OpenAPI
    r.Get("/files/:id/stream", c.Stream) // raw net/http for streaming
}

func (c *Files) Stream(w http.ResponseWriter, req *http.Request) {
    // full control of w, status, headers; no audit, no OpenAPI
}
```

Use the raw form when you need to stream, hijack, or otherwise own the
ResponseWriter. Everything else, use typed.

### Worked example: serving an avatar image

Typical case for the raw form: returning a file with a specific
`Content-Type` and headers. The typed `Out = []byte` shape always emits
`application/octet-stream`, which browsers won't render inline as an
image — for that you want full control of the response.

```go
// avatars/avatars.go
package avatars

import (
    "context"
    "io"
    "net/http"
    "strconv"

    "github.com/amberstack/bosun"
)

// AvatarStore is the data layer — disk, S3, GORM blob column, whatever.
type AvatarStore interface {
    Stat(id string) (*Avatar, error)               // metadata only
    Open(id string) (io.ReadCloser, *Avatar, error) // streamable bytes + metadata
}

type Avatar struct {
    ID          string
    ContentType string // "image/png", "image/jpeg", ...
    Size        int64
    Filename    string
}

type Avatars struct {
    Store AvatarStore   // injected
}

var _ = bosun.Controller[Avatars]("/avatars")

func (c *Avatars) Routes(r *bosun.Router) {
    // Typed metadata endpoint — JSON, audited, OpenAPI'd.
    bosun.Get(r, "/:id/meta", c.Meta)

    // Raw file delivery — full control of headers and streaming.
    r.Get("/:id", c.Serve)
}

// --- typed: metadata as JSON ---

type MetaIn struct {
    ID string `path:"id"`
}

type MetaOut struct {
    ID          string `json:"id"`
    ContentType string `json:"content_type"`
    Bytes       int64  `json:"bytes"`
}

func (c *Avatars) Meta(ctx context.Context, req *bosun.Req[MetaIn]) (MetaOut, error) {
    a, err := c.Store.Stat(req.Body.ID)
    if err != nil {
        return MetaOut{}, bosun.E(http.StatusNotFound, "avatar not found", err)
    }
    return MetaOut{ID: a.ID, ContentType: a.ContentType, Bytes: a.Size}, nil
}

// --- raw: the actual image bytes ---

func (c *Avatars) Serve(w http.ResponseWriter, r *http.Request) {
    id := r.PathValue("id")

    f, a, err := c.Store.Open(id)
    if err != nil {
        http.Error(w, "not found", http.StatusNotFound)
        return
    }
    defer f.Close()

    w.Header().Set("Content-Type", a.ContentType)                    // image/png etc.
    w.Header().Set("Content-Length", strconv.FormatInt(a.Size, 10))
    w.Header().Set("Cache-Control", "public, max-age=3600")
    // Inline (browser displays it). For downloads use "attachment; filename=..."
    w.Header().Set("Content-Disposition", `inline; filename="`+a.Filename+`"`)

    io.Copy(w, f)
}
```

Now:

- `GET /avatars/u_1/meta` → `{"id":"u_1","content_type":"image/png","bytes":12345}`
- `GET /avatars/u_1`      → the raw PNG bytes with the correct `Content-Type`, ready to embed in an `<img>` tag.

The metadata route gets all the typed-handler perks (audit, OpenAPI,
error mapping). The image route is plain `net/http` — no audit event, no
OpenAPI entry, but you get streaming and exact control over the headers
browsers care about.

### Quick variant: returning a small file from a typed handler

If you don't care about `Content-Type` (e.g. you're serving a generic
binary blob and the client knows what to do with it), the typed `[]byte`
shape is enough:

```go
type DownloadIn struct { ID string `path:"id"` }

func (c *Files) Download(ctx context.Context, req *bosun.Req[DownloadIn]) ([]byte, error) {
    b, err := c.Store.Read(req.Body.ID)
    if err != nil {
        return nil, bosun.E(http.StatusNotFound, "not found", err)
    }
    return b, nil   // Content-Type: application/octet-stream
}

bosun.Get(r, "/files/:id", c.Download)
```

Trade-off: you can't override the content-type or stream, but you keep
audit + OpenAPI. Good for "download this attachment" buttons; bad for
inline-rendered images.

See [`files.md`](./files.md) for streaming uploads and the cap-the-body
middleware pattern.

---

## Lifecycle hooks

If the controller (or any injected service) implements
`Init() error`, it's called after injection completes:

```go
func (c *UsersController) Init() error {
    if c.Users == nil {
        return errors.New("UserService missing")
    }
    return nil
}
```

`Init` runs once at app start, after dependencies are wired. Use it for
warm-up, validation, or pre-loading caches.

For shutdown, implement `io.Closer` — `app.Shutdown()` closes services in
reverse dependency order.

---

## Advanced

### Path syntax

Both styles work and can be mixed:

```go
bosun.Get(r, "/users/:id",                c.Get)
bosun.Get(r, "/users/{id}",               c.Get)        // equivalent
bosun.Get(r, "/orgs/:org/users/{user_id}", c.GetMember)  // mix freely
```

Internally `:id` is rewritten to `{id}` (Go's stdlib mux syntax), so the
`path:"id"` tag matches either form.

### Wildcards

For catch-all segments, use Go's wildcard syntax directly:

```go
bosun.Get(r, "/static/{path...}", c.Serve)

type StaticIn struct {
    Path string `path:"path"`
}
```

### Declaring extra error statuses

OpenAPI generation can't always see runtime-computed statuses. Declare
them explicitly:

```go
bosun.Post(r, "/things", c.Create,
    bosun.Errors(http.StatusConflict, http.StatusGone),
)
```

### Per-controller middleware ordering

Middleware runs outermost-first: app → controller-wide → per-route → handler.
Each layer is added in the order you write it.

```go
var _ = bosun.Controller[Admin]("/admin",
    bosun.Use[mw.Logging](),         // logs first (outermost)
    bosun.Use[mw.RequireStaff](),    // then auth check
)

func (c *Admin) Routes(r *bosun.Router) {
    bosun.Post(r, "/wipe", c.Wipe, bosun.Use[mw.DoubleConfirm]())
    // order on /wipe: Logging → RequireStaff → DoubleConfirm → handler
}
```

### Inspecting registered routes

```go
app.Start()
for _, rt := range bosun.TypedRoutes() {
    fmt.Printf("%-6s %s  %s\n", rt.Method, rt.Path, rt.Handler)
}
```

Useful for diagnostics, OpenAPI generation, and route smoke-tests.
