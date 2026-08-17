# Error handling

Bosun maps handler errors to HTTP status codes and JSON bodies with a two-rule model. Return `bosun.E(status, publicMsg, cause)` for a controlled response, and return any other error to get a `500 "internal server error"` whose original message is captured in the audit event but never sent to the client. This guide shows how to use that model well.

<figure class="diagram">
<svg viewBox="0 0 660 220" role="img" aria-labelledby="err-title err-desc" xmlns="http://www.w3.org/2000/svg">
<title id="err-title">Where an error goes</title>
<desc id="err-desc">bosun.E sends its status and public message to the client, while its cause and origin go only to the audit log.</desc>
<defs>
<marker id="err-arw" markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto"><polygon points="0 0, 8 3, 0 6" fill="var(--fg-muted)"/></marker>
</defs>
<line x1="220" y1="86" x2="376" y2="86" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#err-arw)"/>
<line x1="220" y1="154" x2="376" y2="154" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#err-arw)"/>
<text x="298" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="8" letter-spacing="0.06em" fill="var(--fg-muted)">PUBLIC</text>
<text x="298" y="146" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="8" letter-spacing="0.06em" fill="var(--fg-muted)">INTERNAL</text>
<rect x="20" y="64" width="200" height="112" rx="6" fill="var(--accent-soft)" stroke="var(--accent)" stroke-width="1"/>
<text x="120" y="116" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--accent)">bosun.E(...)</text>
<text x="120" y="134" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">status, msg, cause</text>
<rect x="380" y="60" width="240" height="52" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="500" y="82" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Client response</text>
<text x="500" y="100" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">status + public message</text>
<rect x="380" y="128" width="240" height="52" rx="6" fill="var(--code-bg)" stroke="var(--fg-muted)" stroke-width="1"/>
<text x="500" y="150" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Audit log</text>
<text x="500" y="168" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">cause + origin (file:line)</text>
</svg>
<figcaption>A plain error returned from a handler collapses to a 500; either way the cause reaches the audit log, never the client.</figcaption>
</figure>

## The basic pattern

Translate a lower-level error into a status and a public message at the point where you know what it means.

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

The client receives `404` with `{"error":"user not found"}`. The audit event records the same status, the full error including the cause, and the file and line where `bosun.E` was called.

## What bosun.E records

`bosun.E(status, msg, cause)` returns an `*Error` that satisfies the `error` interface. The status is returned to the client, the message becomes the response body `{"error": msg}`, and the cause is recorded for the audit log but never sent to the client (pass `nil` when there is none). `E` also captures the file, line, and function of its own call site, which becomes the `error_origin` in the audit event: exactly the location to look at when triaging a production error.

Because `*Error` is a normal error, it composes with `errors.Is` and `errors.As`, and it can be built deep in a service and returned unchanged through the handler.

```go
func (r *UserRepo) Find(ctx context.Context, id int) (*User, error) {
    var u User
    if err := r.db.WithContext(ctx).First(&u, id).Error; err != nil {
        if errors.Is(err, gorm.ErrRecordNotFound) {
            return nil, bosun.E(http.StatusNotFound, "user not found", err)
        }
        return nil, err // a bare error becomes a 500
    }
    return &u, nil
}
```

Building `*bosun.Error` inside a service couples that service to HTTP. For domain-pure services, return sentinel errors and translate them at the handler boundary instead.

## Centralizing the mapping

Most applications grow one function that turns storage errors into HTTP errors, and call it from every handler.

```go
func mapDBErr(err error) error {
    switch {
    case errors.Is(err, gorm.ErrRecordNotFound):
        return bosun.E(http.StatusNotFound, "not found", err)
    case isUniqueViolation(err):
        return bosun.E(http.StatusConflict, "already exists", err)
    default:
        return bosun.E(http.StatusInternalServerError, "db error", err)
    }
}
```

## What never reaches the client

The cause passed to `bosun.E` is captured in the audit event but never in the response. A plain error returned from a handler is collapsed to `"internal server error"`. The intent is that the client-facing response is chosen by the handler author, not determined by wherever in the call stack an error arose.

## Panics

The typed adapter does not call `recover`, so a panic in a handler is logged by `net/http` and served as an empty 500. To guarantee a JSON 500 and capture a stack trace, add a recover middleware and attach it where you want the safety net.

```go
type Recover struct{}

func (Recover) Handle(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        defer func() {
            if rv := recover(); rv != nil {
                log.Printf("panic: %v\n%s", rv, debug.Stack())
                http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
            }
        }()
        next.ServeHTTP(w, r)
    })
}

var _ = bosun.Middleware[Recover]()
```

## Declaring statuses for OpenAPI

OpenAPI generation infers `200` and any statically visible `bosun.E` calls, but it cannot see a status your handler computes at runtime. Declare those with `bosun.Errors` on the route so they appear in the generated spec.

```go
bosun.Post(r, "/things", c.Create,
    bosun.Errors(http.StatusConflict, http.StatusGone),
)
```
