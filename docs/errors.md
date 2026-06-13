# Error handling

Bosun maps handler errors to HTTP status codes and JSON bodies. Two rules
to remember:

1. **`bosun.E(status, publicMsg, cause)`** for controlled responses.
2. **Any other error** becomes `500 "internal server error"`. The original
   message is captured in the audit event but never sent to the client.

That's the whole model. Below: how to use it well.

---

## The happy path

```go
func (c *UsersController) Get(ctx context.Context, req *bosun.Req[GetIn]) (UserOut, error) {
    u, err := c.Repo.Find(ctx, req.Body.ID)
    if err != nil {
        if errors.Is(err, gorm.ErrRecordNotFound) {
            return UserOut{}, bosun.E(http.StatusNotFound, "user not found", err)
        }
        return UserOut{}, bosun.E(http.StatusInternalServerError, "lookup failed", err)
    }
    return UserOut{ID: u.ID}, nil
}
```

The client sees:

```http
HTTP/1.1 404 Not Found
Content-Type: application/json

{"error":"user not found"}
```

The audit event records:

```json
{
  "status": 404,
  "error": "user not found: record not found",
  "error_origin": "users.go:42 (myapp/users.(*UsersController).Get)"
}
```

Public message → response. Full cause + origin → audit only.

---

## `bosun.E` anatomy

```go
type Error struct {
    Status int
    Msg    string
    Cause  error
    // origin is captured automatically (file:line + function)
}

func E(status int, msg string, cause error) *Error
```

- `status` — HTTP status returned to the client.
- `msg` — public message in the response body (`{"error": msg}`).
- `cause` — the underlying error. Captured in the audit log; never sent
  to the client. Pass `nil` if there is no cause.

`E()` walks one stack frame up and records the file, line, and function
where `E()` was called. That's the `error_origin` in the audit event —
exactly the location to grep for when triaging a production error.

---

## Returning structured errors

`*bosun.Error` satisfies `error`, so it composes with `errors.Is` and
`errors.As`. You can also build errors deeper in the call stack and let
them propagate:

```go
// service layer
func (r *UserRepo) Find(ctx context.Context, id int) (*User, error) {
    var u User
    if err := r.db.WithContext(ctx).First(&u, id).Error; err != nil {
        if errors.Is(err, gorm.ErrRecordNotFound) {
            return nil, bosun.E(http.StatusNotFound, "user not found", err)
        }
        return nil, err  // bare error → 500
    }
    return &u, nil
}

// handler — just return whatever the repo gave you
func (c *UsersController) Get(ctx context.Context, req *bosun.Req[GetIn]) (UserOut, error) {
    u, err := c.Repo.Find(ctx, req.Body.ID)
    if err != nil { return UserOut{}, err }   // 404 if repo built one, 500 otherwise
    return UserOut{ID: u.ID}, nil
}
```

Note: building `*bosun.Error` deep in your service layer couples it to
HTTP. For domain-pure services, return sentinel errors (`ErrNotFound`,
`ErrConflict`) from the service and translate at the handler boundary.

---

## Error helpers — patterns that scale

### Centralize the mapping

```go
func mapDBErr(err error) error {
    switch {
    case errors.Is(err, gorm.ErrRecordNotFound):
        return bosun.E(http.StatusNotFound, "not found", err)
    case isUniqueViolation(err):
        return bosun.E(http.StatusConflict, "already exists", err)
    case errors.Is(err, context.Canceled):
        return bosun.E(499, "client closed request", err)
    default:
        return bosun.E(http.StatusInternalServerError, "db error", err)
    }
}

func (c *UsersController) Create(ctx context.Context, req *bosun.Req[CreateUserIn]) (UserOut, error) {
    u, err := c.Repo.Create(ctx, req.Body)
    if err != nil { return UserOut{}, mapDBErr(err) }
    return UserOut{ID: u.ID}, nil
}
```

### Bubble validation errors

```go
type ValidationError struct{ Field, Reason string }

func (e ValidationError) Error() string { return e.Field + ": " + e.Reason }

func (c *UsersController) Create(ctx context.Context, req *bosun.Req[CreateUserIn]) (UserOut, error) {
    if req.Body.Email == "" {
        return UserOut{}, bosun.E(http.StatusUnprocessableEntity, "email is required",
            ValidationError{Field: "email", Reason: "empty"})
    }
    ...
}
```

The audit record sees the typed validation error; the client sees only
`"email is required"`.

---

## Status code table

These cover ~99% of usage:

| Status | When to use                                              |
|--------|----------------------------------------------------------|
| 400    | Malformed request (bad JSON, invalid path param)         |
| 401    | Missing or invalid auth credentials                      |
| 403    | Authenticated but not allowed                            |
| 404    | Resource doesn't exist                                   |
| 409    | Conflict (duplicate, optimistic-lock failure)            |
| 410    | Resource permanently gone                                |
| 422    | Well-formed but semantically invalid (validation)         |
| 429    | Rate limited                                              |
| 500    | Anything you didn't handle — let Bosun produce this      |

For unknown statuses or anything dynamic, the integer works the same way:
`bosun.E(418, "i'm a teapot", nil)`.

---

## Declaring extra statuses for OpenAPI

OpenAPI generation reads `RouteInfo.Declared` for routes whose statuses
can't be inferred from source. Use `bosun.Errors(...)`:

```go
bosun.Post(r, "/things", c.Create,
    bosun.Errors(http.StatusConflict, http.StatusGone),
)
```

`200` and any statically-visible `bosun.E(...)` calls are picked up
automatically; this is for runtime-computed codes.

---

## What never leaks to the client

- The `cause` passed to `bosun.E` — captured in audit, never in response.
- Plain `error` values returned from a handler — collapsed to
  `"internal server error"`.
- Panics inside a typed handler — the adapter doesn't recover; configure a
  recover middleware at the controller or app level if you want one.

The intent is: client responses are determined by the handler author, not
by where in the call stack an error happened.

---

## Auditing errors

If you've registered a `bosun.Auditor`, every typed request emits an
`AuditEvent`. Errored requests carry:

- `Err` — the full error string (including `cause`).
- `ErrOrigin` — `file:line (func)` of the `bosun.E(...)` call, when
  applicable.

That's enough to pinpoint the raising location without log scraping. See
[`typed-handlers.md`](./typed-handlers.md#auditing).

---

## Panics

The typed adapter does not `recover()`. A panic in a handler crashes the
goroutine handling that request — `net/http` will log it and serve an
empty `500` to the client.

If you want a guaranteed `500` JSON response and a captured stack trace,
add a recover middleware at the app or controller level:

```go
type Recover struct{}
func (Recover) Handle(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        defer func() {
            if rv := recover(); rv != nil {
                log.Printf("panic: %v\n%s", rv, debug.Stack())
                http.Error(w, `{"error":"internal server error"}`, 500)
            }
        }()
        next.ServeHTTP(w, r)
    })
}
var _ = bosun.Middleware[Recover]()
```

Attach app-wide via every controller declaration, or only on the
controllers where you want the safety net.
