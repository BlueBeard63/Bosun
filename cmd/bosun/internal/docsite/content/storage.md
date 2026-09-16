# Object storage

Bosun provides a driver-agnostic object store for blobs such as uploads, documents, and generated files. Services depend on `storemod.Store` to read and write objects by key, and a driver binds that interface to local disk, an S3-compatible service, or WebDAV. Swapping the backend never changes application code, the same way the repo module swaps databases.

<figure class="diagram">
<svg viewBox="0 0 640 210" role="img" aria-labelledby="st-title st-desc" xmlns="http://www.w3.org/2000/svg">
<title id="st-title">One store interface, swappable drivers</title>
<desc id="st-desc">storemod.Store is bound by DefaultBind to the local filesystem, S3, or WebDAV driver.</desc>
<defs>
<marker id="st-arw" markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto"><polygon points="0 0, 8 3, 0 6" fill="var(--fg-muted)"/></marker>
</defs>
<text x="290" y="28" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="8" letter-spacing="0.1em" fill="var(--fg-muted)">DEFAULTBIND (HOST WINS)</text>
<line x1="212" y1="72" x2="378" y2="72" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#st-arw)"/>
<line x1="212" y1="112" x2="378" y2="112" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#st-arw)"/>
<line x1="212" y1="152" x2="378" y2="152" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#st-arw)"/>
<rect x="20" y="40" width="192" height="150" rx="6" fill="var(--accent-soft)" stroke="var(--accent)" stroke-width="1"/>
<text x="116" y="108" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="14" font-weight="600" fill="var(--accent)">storemod.Store</text>
<text x="116" y="128" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">Put / Get / List / Presign</text>
<rect x="380" y="56" width="280" height="32" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="396" y="76" font-family="Inter,system-ui,sans-serif" font-size="12" font-weight="600" fill="var(--fg)">storefsmod (local FS, default)</text>
<rect x="380" y="96" width="280" height="32" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="396" y="116" font-family="Inter,system-ui,sans-serif" font-size="12" font-weight="600" fill="var(--fg)">stores3mod (S3 / MinIO / R2)</text>
<rect x="380" y="136" width="280" height="32" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="396" y="156" font-family="Inter,system-ui,sans-serif" font-size="12" font-weight="600" fill="var(--fg)">storewebdavmod (WebDAV / NextCloud)</text>
</svg>
<figcaption>Application code depends only on the interface; importing a driver and calling For() rebinds it.</figcaption>
</figure>

## The Store interface

A store reads and writes objects by key. `Get` returns `storemod.ErrNotFound` when the key is absent, and `Presign` returns `storemod.ErrUnsupported` on drivers that cannot sign URLs.

```go
type Store interface {
    Put(ctx context.Context, key string, r io.Reader, opts ...PutOption) error
    Get(ctx context.Context, key string) (io.ReadCloser, *Object, error)
    Delete(ctx context.Context, key string) error
    List(ctx context.Context, prefix string) ([]Object, error)
    Presign(ctx context.Context, key string, op Op, ttl time.Duration) (string, error)
}
```

Inject it and use it like any other service.

```go
type Documents struct {
    Store storemod.Store // injected
}

func (d *Documents) Save(ctx context.Context, id string, r io.Reader) error {
    return d.Store.Put(ctx, "documents/"+id, r, storemod.WithContentType("application/pdf"))
}
```

## Selecting a driver

Import a driver and call its `For()` at init. The local filesystem driver is the natural default for development and tests.

```go
import _ "github.com/bluebeard63/bosun/modules/storefsmod"

var _ = storefsmod.For()
```

For S3, configure an `*Options` instance in `main`. The S3 driver works with AWS S3, MinIO, and Cloudflare R2 through the same client.

```go
registry.RegisterInstance[*stores3mod.Options](app.Reg, &stores3mod.Options{
    Endpoint: "s3.amazonaws.com", Bucket: "app-docs",
    AccessKey: os.Getenv("AWS_ACCESS_KEY_ID"),
    SecretKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
    UseSSL:    true,
})
```

## Presigned URLs

A presigned URL lets a client upload or download an object directly, without proxying the bytes through your service. S3 signs URLs natively. The filesystem driver signs an HMAC URL that you verify in your own download handler with `storefsmod.Verify`, and returns `ErrUnsupported` when no signing secret is configured. WebDAV has no signed-URL mechanism and always returns `ErrUnsupported`.

```go
url, err := store.Presign(ctx, "documents/42.pdf", storemod.OpGet, 15*time.Minute)
```

## Drivers at a glance

| Driver | Backend | Presign |
| --- | --- | --- |
| `storefsmod` | local disk | HMAC-signed URL (needs a secret) |
| `stores3mod` | S3, MinIO, R2 | native |
| `storewebdavmod` | WebDAV, NextCloud | unsupported |

Every driver passes the shared `storemodtest` conformance suite, so behavior is consistent across backends.
