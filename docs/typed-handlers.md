# Typed handlers

This is the reference for the typed registration functions (`bosun.Get`, `Post`, `Put`, `Delete`, `Patch`) and the `Req[In]` request wrapper. It describes the handler shape, how the `In` type controls body parsing, how the `Out` type controls the response, and the tag-binding rules.

<figure class="diagram">
<svg viewBox="0 0 620 250" role="img" aria-labelledby="th-title th-desc" xmlns="http://www.w3.org/2000/svg">
<title id="th-title">Binding sources fill Req[In].Body</title>
<desc id="th-desc">Path, query, header, form, and JSON sources each bind into the corresponding fields of the request body struct through struct tags.</desc>
<defs>
<marker id="th-arw" markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto"><polygon points="0 0, 8 3, 0 6" fill="var(--fg-muted)"/></marker>
</defs>
<line x1="202" y1="40" x2="378" y2="40" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#th-arw)"/>
<line x1="202" y1="80" x2="378" y2="80" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#th-arw)"/>
<line x1="202" y1="120" x2="378" y2="120" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#th-arw)"/>
<line x1="202" y1="160" x2="378" y2="160" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#th-arw)"/>
<line x1="202" y1="200" x2="378" y2="200" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#th-arw)"/>
<rect x="20" y="24" width="182" height="32" rx="6" fill="var(--code-bg)" stroke="var(--fg-muted)" stroke-width="1"/>
<text x="34" y="44" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="10" fill="var(--fg)">path:"id"</text>
<rect x="20" y="64" width="182" height="32" rx="6" fill="var(--code-bg)" stroke="var(--fg-muted)" stroke-width="1"/>
<text x="34" y="84" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="10" fill="var(--fg)">query:"limit"</text>
<rect x="20" y="104" width="182" height="32" rx="6" fill="var(--code-bg)" stroke="var(--fg-muted)" stroke-width="1"/>
<text x="34" y="124" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="10" fill="var(--fg)">header:"X-Trace"</text>
<rect x="20" y="144" width="182" height="32" rx="6" fill="var(--code-bg)" stroke="var(--fg-muted)" stroke-width="1"/>
<text x="34" y="164" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="10" fill="var(--fg)">form:"token"</text>
<rect x="20" y="184" width="182" height="32" rx="6" fill="var(--code-bg)" stroke="var(--fg-muted)" stroke-width="1"/>
<text x="34" y="204" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="10" fill="var(--fg)">json:"name"</text>
<rect x="380" y="24" width="200" height="192" rx="6" fill="var(--accent-soft)" stroke="var(--accent)" stroke-width="1"/>
<text x="480" y="116" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="14" font-weight="600" fill="var(--accent)">Req[In].Body</text>
<text x="480" y="136" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">your struct</text>
</svg>
<figcaption>Each tag names one source; the first non-empty of path, query, header, and form wins per field.</figcaption>
</figure>

## Handler shape

Every typed handler has the same signature.

```go
func(ctx context.Context, req *bosun.Req[In]) (Out, error)
```

`ctx` is the request context. `req` wraps the raw `*http.Request` together with a parsed `Body` of type `In`. `Out` is your response type, whose encoding depends on its Go type (see below). Returning `bosun.E(status, publicMsg, cause)` produces a controlled error response; returning any other error produces a 500 with the cause captured for the audit log only. Error handling has its own [guide](./errors.md).

## The Req wrapper

`Req[In]` embeds `*http.Request`, so every method and field of the request is promoted and available directly.

```go
type Req[In any] struct {
    *http.Request     // embedded: all of net/http is available
    Body In           // parsed request body
}
```

```go
auth := req.Header.Get("Authorization")
host := req.Host
raw := req.Request.Body // the underlying io.ReadCloser
```

`req.Body` is the parsed-body field and shadows the embedded request body; reach the raw stream through `req.Request.Body`. Two shortcuts read request values without declaring a struct field: `req.Query()` returns the `url.Values` for query parameters, and `req.Params` exposes the route's path parameters as a `map[string]string`.

## Choosing the In type

The `In` type selects how the request body is parsed.

A **struct** is JSON-decoded (or form-decoded, depending on content type) into the body, after which per-field tag binding fills in path, query, header, and form values. A **`string`** receives the raw body verbatim with no parsing, which is what you want for webhooks that must hash the exact bytes. A **`struct{}`** skips body parsing entirely, for routes with no input. An **`any`** decodes loose JSON into the natural Go type such as `map[string]any`, with no tag binding.

```go
type CreateUserIn struct {
    OrgID    int    `path:"org_id"`      // /orgs/{org_id}/users
    Source   string `query:"source"`     // ?source=invite
    APIKey   string `header:"X-API-Key"` // request header
    Captcha  string `form:"captcha"`     // form-encoded body
    Email    string `json:"email"`       // JSON body field
    Password string `json:"password"`
}
```

When a field has more than one binding tag, the first non-empty source wins in the order path, query, header, form. A field with only a `json:` tag is set by the body decode.

## Choosing the Out type

The `Out` type selects how the response is encoded, symmetric with `In`.

| `Out` type | Response |
| --- | --- |
| struct, map, slice, number, bool | JSON, `Content-Type: application/json` |
| `string` | raw bytes, `text/plain; charset=utf-8` |
| `[]byte` | raw bytes, `application/octet-stream` |
| `struct{}` | no body, status only (for example 204) |

```go
func (c *Health) Status(ctx context.Context, _ *bosun.Req[struct{}]) (string, error) {
    return "ok", nil
}

func (c *Items) Delete(ctx context.Context, req *bosun.Req[DeleteIn]) (struct{}, error) {
    return struct{}{}, c.db.Delete(req.Body.ID)
}
```

For a custom `Content-Type` or streaming, use the raw router methods instead; the [files guide](./files.md) shows a worked example.

## Body content types

The body decoder chooses its strategy from the request `Content-Type`. `application/json` (or an empty content type) is JSON-decoded into the body. Both `application/x-www-form-urlencoded` and `multipart/form-data` are parsed with `ParseForm`, which populates `form:` tags but does not JSON-decode. Any other content type skips body decoding, though tag-based fields are still bound. File uploads are read through `req.MultipartReader()` or `req.FormFile(...)` yourself, since `form:` tags bind only string fields.

## Tag binding reference

```go
type Example struct {
    UserID int    `path:"user_id"`   // from /users/{user_id}
    Limit  int    `query:"limit"`    // from ?limit=50
    Trace  string `header:"X-Trace"` // from a request header
    Token  string `form:"token"`     // from a form-encoded body
    Name   string `json:"name"`      // from a JSON body field
}
```

Path, query, header, and form tags support these scalar kinds: `string`, the signed integers, `bool`, `float32`, and `float64`. Any other kind (slices, maps, unsigned integers, `time.Time`) is a bind-time error, so for richer parsing take the value as a string and parse it in the handler.

## Auditing

When an `Auditor` is registered, every typed request emits an `AuditEvent` containing request metadata, a redacted snapshot of `req.Body`, and either a redacted snapshot of the response or the full error with its origin. A field is redacted when it is tagged `audit:"-"` or when its name contains a sensitive word such as `password`, `secret`, `token`, or `authorization`. Only `req.Body` is walked, so headers and cookies never leak unless you copy them into the body through a binding tag. The [errors guide](./errors.md) covers the error side of the audit event.
