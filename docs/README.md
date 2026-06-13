# Bosun docs

Short, focused pages — start at the top, jump to whatever you need.

## Start here

- [**Getting started**](./getting-started.md) — smallest end-to-end app, line by line.
- [**API overview**](./api-overview.md) — tour of every public symbol you'll call.

## Core concepts

- [**Controllers**](./controllers.md) — registering routes; basic → advanced.
- [**Services**](./services.md) — DI, lifecycle, hot reload; basic → advanced.
- [**Service options**](./service-options.md) — defining a config value once (salt rounds, JWT secret, etc.) and injecting it everywhere.
- [**Middleware**](./middleware.md) — including auth + typed context values; basic → advanced.
- [**Typed handlers**](./typed-handlers.md) — `Req[In]`, body parsing, response shapes, tag binding.
- [**Errors**](./errors.md) — `bosun.E`, status mapping, public vs internal messages.
- [**Convert**](./convert.md) — `bosun.Convert[Dst, Src]` for DB-model ↔ API-DTO mapping.

## Inputs

- [**Forms**](./forms.md) — URL-encoded and multipart-form fields.
- [**Files**](./files.md) — uploads (small + streaming), downloads.
- Query params — see `query:` tag in [`typed-handlers.md`](./typed-handlers.md#tag-binding-reference).

## Data layer

- [**GORM**](./database-gorm.md) — typical Bosun + GORM setup.
- [**sqlc**](./database-sqlc.md) — typical Bosun + sqlc setup.

## Ops & operations

- [**Config & hot reload**](./config.md) — `Dynamic[T]`, file/env/DB sources, live reload.
- [**Testing**](./testing.md) — driving the app from Go tests with stubs.

## Quick reference

### Route registration

```go
// Typed (99% of routes):
bosun.Get(r,    "/things/:id", c.Get)
bosun.Post(r,   "/things",     c.Create)
bosun.Put(r,    "/things/:id", c.Update)
bosun.Delete(r, "/things/:id", c.Delete)
bosun.Patch(r,  "/things/:id", c.Patch)

// Untyped escape hatch (streaming/hijack/file downloads):
r.Get("/stream", c.Stream)
```

Path syntax: `:id` and `{id}` both work.

### Handler shape

```go
func(ctx context.Context, req *bosun.Req[In]) (Out, error)
```

`In` controls body parsing: struct (typed + binding), `string` (raw),
`struct{}` (none), `any` (loose JSON).

`Out` controls response encoding: struct (JSON), `string` (text/plain),
`[]byte` (octet-stream), `struct{}` (no body).

### Tag binding

```go
type Example struct {
    UserID  int    `path:"user_id"`    // from /users/{user_id} or /users/:user_id
    Limit   int    `query:"limit"`     // from ?limit=50
    Trace   string `header:"X-Trace"`  // from request header
    Token   string `form:"token"`      // from x-www-form-urlencoded body
    Name    string `json:"name"`       // from JSON body field
}
```

### Registration declarations

```go
var _ = bosun.Service[T]()                       // injectable singleton
var _ = bosun.Middleware[T]()                    // service + Handle check
var _ = bosun.Controller[T]("/prefix")           // service + Routes mount
var _ = bosun.Default[T](buildFn)                // overridable default provider
var _ = bosun.DefaultBind[Iface, Impl]()         // bind interface to impl
var _ = bosun.DefaultDynamic[T](buildFn)         // hot-reloadable default
var _ = config.Bind[T]("key")                    // JSON key → *Dynamic[T]
```

### Errors

```go
return Out{}, bosun.E(http.StatusNotFound, "user not found", causeErr)
```

`status` + `publicMsg` go to the client. `cause` goes to the audit log
only (with file:line origin).

### Struct mapping

```go
type User struct { ID, Name, Email, Password string }
type UserOut struct { ID, Name, Email string }

out, _ := bosun.Convert[UserOut](user)   // Password isn't on UserOut → can't leak
```

Match by exact name; override with `convert:"Other"`; skip with `convert:"-"`.

### Typed context values

```go
// In middleware:
ctx := bosun.WithValue(r.Context(), user)   // user is *AuthUser
next.ServeHTTP(w, r.WithContext(ctx))

// In handler:
u := bosun.Value[AuthUser](ctx)   // *AuthUser
```

Type is the key — no string slots, no collisions.

---

## Where to look next

- The kitchen-sink example app: [`examples/kitchen-sink/main.go`](../examples/kitchen-sink/main.go) — covers auditor + encrypted config + typed routes + rate limiting.
- The built-in middleware: [`mw/`](../mw/).
- The bundled modules: [`modules/`](../modules/).
