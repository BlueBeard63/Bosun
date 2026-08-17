# File handling

Bosun adds nothing on top of `net/http` for files: the embedded `*http.Request` in `Req[In]` gives you `FormFile`, `MultipartReader`, and direct body access. This guide shows the standard upload and download patterns and the gotchas around body parsing.

## Small uploads

For a single file where buffering in memory is acceptable, bind the text fields with `form:` tags and read the file part with `req.FormFile`.

```go
type UploadIn struct {
    Title string `form:"title"`
}

func (c *Avatars) Upload(ctx context.Context, req *bosun.Req[UploadIn]) (UploadOut, error) {
    f, hdr, err := req.FormFile("file")
    if err != nil {
        return UploadOut{}, bosun.E(http.StatusBadRequest, "missing file", err)
    }
    defer f.Close()

    id, err := c.store.Save(hdr.Filename, hdr.Header.Get("Content-Type"), f)
    if err != nil {
        return UploadOut{}, bosun.E(http.StatusInternalServerError, "save failed", err)
    }
    return UploadOut{ID: id}, nil
}
```

`FormFile` parses the whole request into memory or temporary files before returning, so it is not suitable for streaming.

## Large or streaming uploads

For gigabyte payloads or back-pressure to the client, use `req.MultipartReader` and stream each part straight to storage. This requires that nothing has already parsed the body, so declare `In` as `struct{}` to stop the framework from parsing it.

```go
func (c *Bulk) Upload(ctx context.Context, req *bosun.Req[struct{}]) (BulkOut, error) {
    mr, err := req.MultipartReader()
    if err != nil {
        return BulkOut{}, bosun.E(http.StatusBadRequest, "not multipart", err)
    }
    for {
        part, err := mr.NextPart()
        if err == io.EOF {
            break
        }
        if err != nil {
            return BulkOut{}, bosun.E(http.StatusBadRequest, "bad multipart", err)
        }
        if part.FormName() == "file" {
            if err := c.store.Stream(part.FileName(), part); err != nil {
                return BulkOut{}, bosun.E(http.StatusInternalServerError, "save failed", err)
            }
        }
        _ = part.Close()
    }
    return BulkOut{}, nil
}
```

The reader is single-pass and cannot rewind, and each `part` is an `io.Reader` you pass directly to your storage backend.

## Raw body uploads

For a PUT where the entire body is the file, read `req.Request.Body` directly. Use `req.Request.Body` rather than `req.Body`, because the outer parsed-body field shadows the embedded request body, and declare `In` as `struct{}` so the framework leaves the body untouched.

```go
func (c *Files) Put(ctx context.Context, req *bosun.Req[struct{}]) (PutOut, error) {
    id, err := c.store.Stream(req.PathValue("name"), req.Request.Body)
    if err != nil {
        return PutOut{}, bosun.E(http.StatusInternalServerError, "save failed", err)
    }
    return PutOut{ID: id}, nil
}
```

## Downloads

Return `[]byte` from a typed handler for a small download; the response is sent as `application/octet-stream`. When you need a specific content type, a `Content-Disposition` header, or streaming, drop to the raw router methods, which hand you the `ResponseWriter` directly.

```go
func (c *Files) Routes(r *bosun.Router) {
    r.Get("/files/:id/download", c.Download) // raw net/http
}

func (c *Files) Download(w http.ResponseWriter, r *http.Request) {
    f, err := c.store.Open(r.PathValue("id"))
    if err != nil {
        http.Error(w, "not found", http.StatusNotFound)
        return
    }
    defer f.Close()
    w.Header().Set("Content-Type", "application/octet-stream")
    w.Header().Set("Content-Disposition", `attachment; filename="`+f.Name+`"`)
    io.Copy(w, f)
}
```

A raw route does not emit an audit event or an OpenAPI entry; that is the trade for full control of the response.

## Limiting request size

Cap the body with `http.MaxBytesReader`, applied in a middleware so it covers typed upload routes. Exceeding the cap surfaces as a parse error and a 400 response.

```go
type LimitBody struct{}

func (LimitBody) Handle(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        r.Body = http.MaxBytesReader(w, r.Body, 50<<20) // 50 MiB
        next.ServeHTTP(w, r)
    })
}

var _ = bosun.Middleware[LimitBody]()
```

## Validating content type

Check the declared content type, but do not trust it alone for security-sensitive flows; sniff the file's magic bytes when it matters.

```go
switch hdr.Header.Get("Content-Type") {
case "image/png", "image/jpeg":
default:
    return UploadOut{}, bosun.E(http.StatusUnsupportedMediaType, "only PNG or JPEG", nil)
}
```

## Auditing and files

Only `req.Body` is walked by the audit redactor, so multipart file bytes never reach the audit log. If you copy a filename or caption into `req.Body`, that value is captured, so tag anything sensitive with `audit:"-"`.
