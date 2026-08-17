# Form handling

Bosun decodes form-encoded bodies automatically when the handler's `In` type is a struct, binding each field through its `form:` tag. This applies to both `application/x-www-form-urlencoded` and the text fields of `multipart/form-data`. File payloads are read separately; see the [files guide](./files.md).

## URL-encoded forms

When the request `Content-Type` is `application/x-www-form-urlencoded`, Bosun skips JSON decoding, calls `req.ParseForm()`, and fills every `form:`-tagged field from the parsed body.

```go
type LoginIn struct {
    Email    string `form:"email"`
    Password string `form:"password"`
    Remember bool   `form:"remember"`
}

func (c *Auth) Login(ctx context.Context, req *bosun.Req[LoginIn]) (LoginOut, error) {
    in := req.Body
    if err := c.auth.Check(in.Email, in.Password); err != nil {
        return LoginOut{}, bosun.E(http.StatusUnauthorized, "invalid credentials", err)
    }
    return LoginOut{Token: "..."}, nil
}
```

The `form:` tag supports the same scalar kinds as the other binding tags: `string`, the signed integers, `bool`, and the floats.

## Multipart form fields

The same `form:` tags bind the text fields of a multipart form. The file parts are read directly from the embedded request with `req.FormFile(...)` or `req.MultipartReader()`, which the [files guide](./files.md) covers.

```go
type UploadIn struct {
    Title       string `form:"title"`
    Description string `form:"description"`
}
```

## Combining sources

One struct can pull from several places at once. When a field carries more than one tag, the first non-empty source wins in the order path, query, header, form. On a form route the JSON branch never runs, so a field that carries only a `json:` tag is left at zero; use `form:` for every field on a form route.

```go
type SignupIn struct {
    InviteCode string `path:"code"`   // from /signup/{code}
    Source     string `query:"src"`   // ?src=email
    Captcha    string `form:"captcha"`
    Email      string `form:"email"`
    Password   string `form:"password"`
}
```

## Validation

Bosun does not ship a validator, so validate in the handler and translate failures into `bosun.E` calls. If you prefer declarative validation, use a library such as `go-playground/validator` inside the handler and map its errors to statuses.

```go
if in.Email == "" {
    return LoginOut{}, bosun.E(http.StatusUnprocessableEntity, "email required", nil)
}
```

## Auditing form fields

Form fields land in `req.Body`, which is what the audit redactor walks, so a field named `password`, `secret`, `token`, or `authorization` is redacted automatically. Tag any other sensitive field with `audit:"-"`.

```go
type LoginIn struct {
    Email    string `form:"email"`
    Password string `form:"password"`      // auto-redacted by name
    OTP      string `form:"otp" audit:"-"` // explicit
}
```

## Repeated fields and edge cases

The binder reads the first value of each field. For checkbox lists or repeated fields, read the underlying `req.PostForm` (a `url.Values`) in the handler. A single Bosun route decodes either JSON or a form based on the content type, not both, so use separate routes for separate content types, or take `In` as `string` or `any` and decode by hand.

```go
tags := req.PostForm["tag"] // []string from ?tag=go&tag=web
```
