# Typed handlers

Reference for the `bosun.Get / Post / Put / Delete / Patch` registration
functions and the `Req[In]` wrapper.

## Handler shape

Every typed handler has the same signature:

```go
func(ctx context.Context, req *bosun.Req[In]) (Out, error)
```

- `ctx` — the request context, scoped to this request.
- `req` — wraps the raw `*http.Request` plus a parsed `Body In`.
- `Out` — your response type. Encoding depends on the type — see "Choosing
  the `Out` type" below. Default is JSON with status 200.
- `error` — return `bosun.E(status, publicMsg, cause)` for a controlled
  status + safe public message. Any other `error` becomes a 500 with
  `"internal server error"` and the cause is captured in the audit event
  but never sent to the client.

Registration:

```go
func (c *MyController) Routes(r *bosun.Router) {
    bosun.Get(r, "/things/{id}", c.GetThing)
    bosun.Post(r, "/things", c.CreateThing, bosun.Errors(http.StatusConflict))
}
```

`bosun.Errors(...)` declares additional status codes the route can return,
for OpenAPI generation in binaries where source scanning isn't available.

### Path syntax

Both `:id` and `{id}` work — the router normalizes `:id` to `{id}` before
handing the route to Go's `net/http` mux. Mix freely:

```go
bosun.Get(r, "/users/:id",                c.GetUser)
bosun.Get(r, "/users/{id}",               c.GetUser)        // equivalent
bosun.Get(r, "/orgs/:org/users/{user_id}", c.GetOrgUser)    // mix is fine
```

Both forms bind the same way: `path:"id"` on a struct field pulls the value
out via `req.PathValue("id")`. The normalized `{id}` form is what
`TypedRoutes()` and OpenAPI generation see.

## `Req[In]` — what's inside

```go
type Req[In any] struct {
    *http.Request   // embedded — all of net/http is available
    Body In         // parsed request body
}
```

Because `*http.Request` is embedded, every method/field is promoted:

```go
auth := req.Header.Get("Authorization")
ck,  _ := req.Cookie("session")
ua   := req.UserAgent()
host := req.Host
tls  := req.TLS
raw  := req.Request.Body          // raw io.ReadCloser (use req.Request to disambiguate from Body In)
```

`req.Request.Body` reaches the underlying `io.ReadCloser`. Plain `req.Body`
refers to the parsed-body field — it shadows the embedded one.

### Query parameter shortcut

For ad-hoc query reads (where you don't want a struct field with a
`query:` tag), `req.Query()` wraps `req.URL.Query()`:

```go
q    := req.Query().Get("q")        // single value
tags := req.Query()["tag"]          // []string for ?tag=a&tag=b
```

It returns the standard `url.Values`, so anything `url.Values` supports
works.

## Choosing the `In` type

`In` controls how the body is parsed. Four shapes:

### 1. A struct — typed body + tag binding

The body is JSON-decoded (or form-decoded — see below) into the struct,
then per-field tag binding fills in path / query / header / form values.

```go
type CreateUserIn struct {
    OrgID    int    `path:"org_id"`        // /orgs/{org_id}/users
    Source   string `query:"source"`       // ?source=invite
    APIKey   string `header:"X-API-Key"`   // request header
    Captcha  string `form:"captcha"`       // form-encoded body
    Email    string `json:"email"`         // JSON body field
    Password string `json:"password"`
}

func (c *Users) Create(ctx context.Context, req *bosun.Req[CreateUserIn]) (UserOut, error) {
    in := req.Body
    // in.OrgID, in.Source, in.APIKey, in.Captcha, in.Email, in.Password all populated
}
```

Tag order within a field: the first non-empty tag wins, in this order:
`path` → `query` → `header` → `form`. A field with no binding tag (just
`json:`) is set by body decode only.

### 2. `string` — raw body

The body is read verbatim into `req.Body` as a string. No JSON parsing,
no tag binding.

```go
func (c *Webhooks) Receive(ctx context.Context, req *bosun.Req[string]) (struct{ OK bool }, error) {
    payload := req.Body                          // raw text/JSON/whatever
    sig := req.Header.Get("X-Signature")         // pull header from req
    if !verify(sig, payload) {
        return struct{ OK bool }{}, bosun.E(401, "bad signature", nil)
    }
    return struct{ OK bool }{OK: true}, nil
}
```

Useful for webhooks where you need to hash the exact bytes before parsing,
or for text/plain endpoints.

### 3. `struct{}` — no body expected

The framework skips body parsing entirely. Junk in the body is ignored
(not an error). Use this for GETs that have no input or for routes where
everything you need is on `req`.

```go
func (c *Auth) Whoami(ctx context.Context, req *bosun.Req[struct{}]) (WhoOut, error) {
    token := req.Header.Get("Authorization")
    return c.lookup(token)
}
```

### 4. `any` — loose JSON

JSON-decoded into `req.Body` as the natural Go type
(`map[string]any`, `[]any`, etc.). No tag binding. Empty body is fine.

```go
func (c *Events) Log(ctx context.Context, req *bosun.Req[any]) (struct{ OK bool }, error) {
    if m, ok := req.Body.(map[string]any); ok {
        // poke at m["type"], m["payload"], ...
    }
    return struct{ OK bool }{OK: true}, nil
}
```

## Choosing the `Out` type

`Out` controls how the response is encoded. Four shapes, symmetric with `In`:

| `Out` type            | Response                                                       |
| --------------------- | -------------------------------------------------------------- |
| any struct / map / slice / scalar number / bool | JSON-encoded with `Content-Type: application/json` |
| `string`              | Raw bytes, `Content-Type: text/plain; charset=utf-8`           |
| `[]byte`              | Raw bytes, `Content-Type: application/octet-stream`            |
| `struct{}`            | No body, status only — useful for 204 / pure-side-effect routes |

Examples:

```go
// JSON (default)
func (c *Users) Get(ctx context.Context, req *Req[GetIn]) (User, error) { ... }

// Plain text
func (c *Health) Status(ctx context.Context, _ *Req[struct{}]) (string, error) {
    return "ok", nil
}

// Binary
func (c *Avatar) Get(ctx context.Context, req *Req[GetIn]) ([]byte, error) {
    return c.store.Read(req.Body.ID)
}

// No body
func (c *Items) Delete(ctx context.Context, req *Req[DeleteIn]) (struct{}, error) {
    return struct{}{}, c.db.Delete(req.Body.ID)
}
```

For richer control (custom `Content-Type`, streaming, etc.) use the untyped
escape hatch — see end of doc.

## Body content-types

`bind()` chooses the body decode strategy from `Content-Type`:

| Content-Type                            | Behavior                                              |
| --------------------------------------- | ----------------------------------------------------- |
| `application/json`, empty               | JSON-decode body into `Body`                          |
| `application/x-www-form-urlencoded`     | `ParseForm()`; populate `form:` tags. No JSON decode. |
| `multipart/form-data`                   | `ParseForm()`; populate `form:` tags. No JSON decode. |
| anything else                           | No body decode; tag-based fields still bound.         |

For multipart **file uploads** you still call `req.MultipartReader()` or
`req.FormFile(...)` yourself — `form:` tags only do string fields.

## Tag binding reference

```go
type Example struct {
    UserID  int    `path:"user_id"`   // from /users/{user_id}
    Limit   int    `query:"limit"`    // from ?limit=50
    Trace   string `header:"X-Trace"` // from request header
    Token   string `form:"token"`     // from x-www-form-urlencoded body
    Name    string `json:"name"`      // from JSON body field
}
```

Supported scalar kinds for path / query / header / form:
`string`, `int` / `int8` / `int16` / `int32` / `int64`, `bool`, `float32`,
`float64`. Anything else (slices, maps, uints, time.Time) is an error at
bind time — for richer parsing, take the value as a string and parse in
the handler.

## Errors

Use `bosun.E(status, publicMessage, cause)`:

```go
if err := c.auth.Check(in.Email, in.Password); err != nil {
    return UserOut{}, bosun.E(http.StatusUnauthorized, "invalid credentials", err)
}
```

- `status` is what the client sees.
- `publicMessage` is what the client reads in the JSON response.
- `cause` is captured in the audit event with the file:line origin of
  this `E(...)` call, but never sent to the client.

A plain `errors.New(...)` returned from a handler becomes a 500 with
`"internal server error"`. The original message is captured in the audit
event, not in the response.

`bosun.Errors(http.StatusConflict, http.StatusGone)` as a route option
declares additional status codes for OpenAPI generation when source
scanning can't see runtime-computed statuses.

## Auditing

If you register an `Auditor` (`registry.RegisterInstance[bosun.Auditor]`),
every typed request emits an `AuditEvent` with:

- request metadata (method, path, status, duration, remote addr),
- a redacted snapshot of `req.Body`,
- a redacted snapshot of the response (or the full error + origin file:line).

Redaction rules:
- Field tagged `audit:"-"` → `"[REDACTED]"`.
- Field name containing `password`, `secret`, `token`, `apikey`,
  `api_key`, `authorization` (case-insensitive) → `"[REDACTED]"`.

```go
type LoginIn struct {
    Email    string `json:"email"`
    Password string `json:"password"`               // auto-redacted (name)
    APIKey   string `json:"api_key" audit:"-"`      // explicit
}
```

The raw `*http.Request` is **not** walked by Redact — only `req.Body` is.
That's why headers / cookies don't leak into the audit log by default;
if you copy a sensitive header into `Body` via a `header:` tag, give the
field a redact-worthy name (e.g. `Authorization`) or tag it `audit:"-"`.

## Cookbook

### GET with path + query params

```go
type ListIn struct {
    OrgID int    `path:"org_id"`
    Limit int    `query:"limit"`
    Q     string `query:"q"`
}

bosun.Get(r, "/orgs/{org_id}/users", c.List)

func (c *Users) List(ctx context.Context, req *bosun.Req[ListIn]) (ListOut, error) { ... }
```

### POST with JSON body + header

```go
type CreateIn struct {
    Idempotency string `header:"Idempotency-Key"`
    Name        string `json:"name"`
}

bosun.Post(r, "/things", c.Create)
```

### Webhook with HMAC verification

```go
func (c *GH) Hook(ctx context.Context, req *bosun.Req[string]) (struct{ OK bool }, error) {
    sig := req.Header.Get("X-Hub-Signature-256")
    if !hmacOK(sig, []byte(req.Body), c.secret) {
        return struct{ OK bool }{}, bosun.E(401, "bad signature", nil)
    }
    var payload GitHubEvent
    if err := json.Unmarshal([]byte(req.Body), &payload); err != nil {
        return struct{ OK bool }{}, bosun.E(400, "bad payload", err)
    }
    return struct{ OK bool }{OK: true}, c.process(ctx, &payload)
}
```

### Form-encoded login

```go
type FormLoginIn struct {
    Email    string `form:"email"`
    Password string `form:"password"`
}

bosun.Post(r, "/login", c.FormLogin)

func (c *Auth) FormLogin(ctx context.Context, req *bosun.Req[FormLoginIn]) (LoginOut, error) {
    in := req.Body
    // ...
}
```

Client sends `Content-Type: application/x-www-form-urlencoded` with
`email=jack%40x.dev&password=hunter2`.

### Bodyless route

```go
bosun.Get(r, "/health", c.Health)

func (c *Health) Health(ctx context.Context, _ *bosun.Req[struct{}]) (HealthOut, error) {
    return HealthOut{OK: true}, nil
}
```

### Loose JSON pass-through

```go
bosun.Post(r, "/proxy/events", c.Forward)

func (c *Proxy) Forward(ctx context.Context, req *bosun.Req[any]) (struct{ OK bool }, error) {
    return struct{ OK bool }{OK: true}, c.upstream.Send(ctx, req.Body)
}
```

## Untyped escape hatch

If you don't want the typed adapter at all — say you need to stream a
response or hijack the connection — use the router's plain methods:

```go
func (c *Files) Routes(r *bosun.Router) {
    r.Get("/download/{id}", c.Download)
}

func (c *Files) Download(w http.ResponseWriter, r *http.Request) {
    // raw net/http handler — no Req, no body parsing, no audit event
}
```

You give up audit + OpenAPI + typed binding; you get full control of the
response writer.
