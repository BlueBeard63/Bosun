# File handling

Bosun adds nothing on top of `net/http` for files: the embedded `*http.Request` in `Req[In]` gives you `FormFile`, `MultipartReader`, and direct body access. This guide shows the standard upload and download patterns and the gotchas around body parsing.

<figure class="diagram">
<svg viewBox="0 0 700 200" role="img" aria-labelledby="fl-title fl-desc" xmlns="http://www.w3.org/2000/svg">
<title id="fl-title">Two ways to read an upload</title>
<desc id="fl-desc">FormFile buffers the whole request in memory or temp files for small uploads; MultipartReader streams each part for large uploads.</desc>
<defs>
<marker id="fl-arw" markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto"><polygon points="0 0, 8 3, 0 6" fill="var(--fg-muted)"/></marker>
</defs>
<line x1="200" y1="76" x2="378" y2="76" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#fl-arw)"/>
<line x1="200" y1="124" x2="378" y2="124" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#fl-arw)"/>
<text x="289" y="68" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="8" letter-spacing="0.06em" fill="var(--fg-muted)">SMALL</text>
<text x="289" y="116" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="8" letter-spacing="0.06em" fill="var(--fg-muted)">STREAMING</text>
<rect x="20" y="48" width="180" height="104" rx="6" fill="var(--code-bg)" stroke="var(--fg-muted)" stroke-width="1"/>
<text x="110" y="96" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Multipart request</text>
<text x="110" y="114" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">one or more files</text>
<rect x="380" y="54" width="300" height="44" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="530" y="76" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">req.FormFile</text>
<text x="530" y="92" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">buffered in memory / temp</text>
<rect x="380" y="102" width="300" height="44" rx="6" fill="var(--accent-soft)" stroke="var(--accent)" stroke-width="1"/>
<text x="530" y="124" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--accent)">req.MultipartReader</text>
<text x="530" y="140" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">streamed part by part</text>
</svg>
<figcaption>MultipartReader requires an unparsed body, so declare In as struct{} when you stream.</figcaption>
</figure>

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
