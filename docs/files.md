# File handling

Bosun doesn't add anything on top of `net/http` for files — the embedded
`*http.Request` in `Req[In]` gives you `FormFile`, `MultipartReader`, and
direct body access. This doc shows the standard patterns and the gotchas.

---

## Small uploads — `req.FormFile`

For a single file under ~10 MB, where buffering in memory is fine:

```go
type UploadIn struct {
    Title string `form:"title"`
}

type UploadOut struct {
    ID string `json:"id"`
}

func (c *Avatars) Upload(ctx context.Context, req *bosun.Req[UploadIn]) (UploadOut, error) {
    // Title is already bound from the form fields.
    // The file lives under the "file" multipart part:
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

bosun.Post(r, "/avatars", c.Upload)
```

### Client

```
POST /avatars HTTP/1.1
Content-Type: multipart/form-data; boundary=----abc

------abc
Content-Disposition: form-data; name="title"

Profile pic
------abc
Content-Disposition: form-data; name="file"; filename="me.png"
Content-Type: image/png

<bytes>
------abc--
```

`FormFile` internally calls `ParseMultipartForm(32 << 20)` (32 MiB default).
Larger files spill to disk in a temp dir. Don't use this for streaming.

---

## Large uploads / streaming — `req.MultipartReader`

`FormFile` buffers the entire request into memory or temp files before
returning. For streaming uploads, gigabyte payloads, or back-pressure to
the client, use `MultipartReader`:

```go
func (c *Bulk) Upload(ctx context.Context, req *bosun.Req[struct{}]) (struct{ N int }, error) {
    mr, err := req.MultipartReader()
    if err != nil {
        return struct{ N int }{}, bosun.E(http.StatusBadRequest, "not multipart", err)
    }

    n := 0
    for {
        part, err := mr.NextPart()
        if err == io.EOF { break }
        if err != nil {
            return struct{ N int }{}, bosun.E(http.StatusBadRequest, "bad multipart", err)
        }

        switch part.FormName() {
        case "file":
            // stream straight to disk / S3 / whatever
            if err := c.store.Stream(part.FileName(), part); err != nil {
                return struct{ N int }{}, bosun.E(http.StatusInternalServerError, "save failed", err)
            }
            n++
        default:
            _ = part.Close()   // skip
        }
    }
    return struct{ N int }{N: n}, nil
}
```

Notes:
- `MultipartReader` requires that **no** prior call has touched `ParseForm`
  / `FormFile`. Use `In = struct{}` so the framework doesn't try to parse
  the body itself.
- The reader is single-pass; you can't rewind.
- `part` is an `io.Reader` — pass it directly to your storage backend.

---

## Raw uploads (no multipart)

For PUT-style "the body is the file" uploads:

```go
func (c *Files) Put(ctx context.Context, req *bosun.Req[struct{}]) (struct{ ID string }, error) {
    id, err := c.store.Stream(req.PathValue("name"), req.Request.Body)
    if err != nil {
        return struct{ ID string }{}, bosun.E(500, "save failed", err)
    }
    return struct{ ID string }{ID: id}, nil
}

bosun.Put(r, "/files/:name", c.Put)
```

Use `req.Request.Body` (not `req.Body`) — the outer `Body` field shadows
the embedded `http.Request.Body`. With `In = struct{}` the framework
doesn't touch the body, so it's still readable.

---

## Downloads

### Small / structured

Return `[]byte`:

```go
type GetIn struct { ID string `path:"id"` }

func (c *Files) Get(ctx context.Context, req *bosun.Req[GetIn]) ([]byte, error) {
    b, err := c.store.Read(req.Body.ID)
    if err != nil {
        return nil, bosun.E(http.StatusNotFound, "not found", err)
    }
    return b, nil    // Content-Type: application/octet-stream
}

bosun.Get(r, "/files/:id", c.Get)
```

### Streaming or custom headers

Drop to the untyped escape hatch — typed routes always finalize the
response after the handler returns, which doesn't suit streaming:

```go
func (c *Files) Routes(r *bosun.Router) {
    r.Get("/files/:id/download", c.Download)   // raw net/http
}

func (c *Files) Download(w http.ResponseWriter, r *http.Request) {
    id := r.PathValue("id")
    f, err := c.store.Open(id)
    if err != nil { http.Error(w, "not found", 404); return }
    defer f.Close()

    w.Header().Set("Content-Type", "application/octet-stream")
    w.Header().Set("Content-Disposition", `attachment; filename="`+f.Name+`"`)
    w.Header().Set("Content-Length", strconv.FormatInt(f.Size, 10))
    io.Copy(w, f)
}
```

Untyped routes don't emit audit events or generate OpenAPI — you give that
up for full control of the response writer.

---

## Validation, limits

### Cap the request size

Wrap the body before reading:

```go
r.Body = http.MaxBytesReader(w, r.Body, 50<<20)   // 50 MiB
```

For typed handlers that use `FormFile`, do this inside a middleware:

```go
type LimitBody struct{}
func (LimitBody) Handle(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        r.Body = http.MaxBytesReader(w, r.Body, 50<<20)
        next.ServeHTTP(w, r)
    })
}
var _ = bosun.Middleware[LimitBody]()
```

Attach with `bosun.Use[LimitBody]()` on the upload route. Exceeding the
cap surfaces as a parse error → `400 Bad Request`.

### Validate MIME type

```go
ct := hdr.Header.Get("Content-Type")
switch ct {
case "image/png", "image/jpeg":
default:
    return Out{}, bosun.E(415, "only PNG or JPEG", nil)
}
```

Don't trust the `Content-Type` header alone — for security-sensitive
flows, sniff the magic bytes from the file itself.

---

## Audit and files

`req.Body` (your typed `In`) is the only thing the audit redactor walks.
Multipart file bytes never end up in the audit log unless you put them
there — but if you copy a filename or caption into `req.Body`, that *is*
captured. Use `audit:"-"` on fields that shouldn't appear in the audit
event.
