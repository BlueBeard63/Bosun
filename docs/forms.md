# Form handling

Bosun decodes form-encoded bodies (`application/x-www-form-urlencoded` and
`multipart/form-data`) automatically when the handler's `In` type is a
struct. Per-field binding uses the `form:` tag.

---

## `application/x-www-form-urlencoded`

The classic HTML form / URL-encoded body.

### Handler

```go
type LoginIn struct {
    Email    string `form:"email"`
    Password string `form:"password"`
    Remember bool   `form:"remember"`
}

type LoginOut struct {
    Token string `json:"token"`
}

func (c *Auth) Login(ctx context.Context, req *bosun.Req[LoginIn]) (LoginOut, error) {
    in := req.Body
    if err := c.auth.Check(in.Email, in.Password); err != nil {
        return LoginOut{}, bosun.E(http.StatusUnauthorized, "invalid credentials", err)
    }
    return LoginOut{Token: "..."}, nil
}

bosun.Post(r, "/login", c.Login)
```

### Client

```
POST /login HTTP/1.1
Content-Type: application/x-www-form-urlencoded

email=jack%40x.dev&password=hunter2&remember=true
```

When `Content-Type` starts with `application/x-www-form-urlencoded`, Bosun:
- skips JSON decoding entirely;
- calls `req.ParseForm()`;
- fills every `form:"name"`-tagged field via `req.PostForm.Get("name")`.

Supported scalar kinds for `form:`: `string`, `int*`, `bool`, `float32/64`.

---

## `multipart/form-data` — fields

The same `form:` tags work for the text fields of a multipart form:

```go
type UploadIn struct {
    Title       string `form:"title"`
    Description string `form:"description"`
}
```

For the actual file payload, you reach for `req.MultipartReader()` or
`req.FormFile(...)` directly on the embedded `*http.Request`. See
[`files.md`](./files.md) for upload patterns.

---

## Mixing form fields with other sources

The same struct can pull from any source via tags. Order: `path` → `query`
→ `header` → `form`. First non-empty tag wins per field:

```go
type SignupIn struct {
    InviteCode string `path:"code"`       // from /signup/{code}
    Source     string `query:"src"`       // ?src=email
    Captcha    string `form:"captcha"`    // from form body
    Email      string `form:"email"`
    Password   string `form:"password"`
}

bosun.Post(r, "/signup/:code", c.Signup)
```

A field with no binding tag (only `json:`) is filled by JSON decode only —
but for form-encoded routes, the JSON branch never runs, so a `json:` tag
alone gets left zero. Use `form:` for everything on form routes.

---

## Validation

Bosun doesn't ship a validator — handle it in the handler:

```go
func (c *Auth) Login(ctx context.Context, req *bosun.Req[LoginIn]) (LoginOut, error) {
    in := req.Body
    if in.Email == "" {
        return LoginOut{}, bosun.E(http.StatusUnprocessableEntity, "email required", nil)
    }
    if !strings.Contains(in.Email, "@") {
        return LoginOut{}, bosun.E(http.StatusUnprocessableEntity, "bad email", nil)
    }
    ...
}
```

If you want declarative validation, drop in `go-playground/validator` in
your handler and translate failures to `bosun.E(...)` calls.

---

## Forms and audit

Form fields land in `req.Body`, which is what the audit redactor walks.
Fields named `password`, `secret`, `token`, `apikey`, `authorization`
(case-insensitive) are auto-redacted. Tag with `audit:"-"` to force
redaction of anything else:

```go
type LoginIn struct {
    Email    string `form:"email"`
    Password string `form:"password"`              // auto-redacted (name)
    OTP      string `form:"otp" audit:"-"`         // explicit
}
```

---

## Advanced

### Multiple values for one field

`req.PostForm` is a `url.Values` (`map[string][]string`). The binder uses
`Get`, which returns only the first value. For checkbox lists or repeated
fields, take the slice as `string` and split yourself, or reach for the
underlying form in the handler:

```go
type FilterIn struct{}  // no binding fields

func (c *Search) Filter(ctx context.Context, req *bosun.Req[FilterIn]) (Out, error) {
    _ = req.ParseForm()  // safe — already parsed by Bosun, but it's a no-op the 2nd time
    tags := req.PostForm["tag"]   // []string{"go", "web"} from ?tag=go&tag=web
    ...
}
```

### Form route with no body fields, just files

If every input is a file, use `In = struct{}` and read files directly:

```go
type Files struct{}
func (c *Files) Upload(ctx context.Context, req *bosun.Req[struct{}]) (Out, error) {
    f, hdr, err := req.FormFile("file")
    if err != nil { return Out{}, bosun.E(400, "missing file", err) }
    defer f.Close()
    ...
}
```

But check [`files.md`](./files.md) — for big uploads you usually want
`MultipartReader()` instead of `FormFile()`.

### Mixing JSON and forms on the same route

You can't, on the same Bosun route — body decode is one or the other based
on `Content-Type`. Use separate routes for separate content types, or
take `In = string` / `In = any` and decode by hand.

### Disabling form parsing

There is no opt-out at the route level. If you don't want auto-parse, use
`In = string` (raw body) or `In = any` (loose JSON), or use the untyped
escape hatch (`r.Post(...)`).
