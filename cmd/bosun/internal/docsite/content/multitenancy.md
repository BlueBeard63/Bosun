# Multi-tenancy

When one deployment serves many customers, each request belongs to a tenant, and a tenant must never see another's data. Bosun resolves the tenant from each request, carries it on the context, and confines a repository to that tenant so queries and writes are scoped automatically. The tenant also rides the event bus, so an event published under one tenant is consumed under the same one.

<figure class="diagram">
<svg viewBox="0 0 720 170" role="img" aria-labelledby="mt-title mt-desc" xmlns="http://www.w3.org/2000/svg">
<title id="mt-title">Tenant scoping</title>
<desc id="mt-desc">The middleware resolves the tenant onto the context; a scoped repo adds a tenant filter to every query against the database.</desc>
<defs>
<marker id="mt-arw" markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto"><polygon points="0 0, 8 3, 0 6" fill="var(--fg-muted)"/></marker>
</defs>
<line x1="172" y1="64" x2="192" y2="64" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#mt-arw)"/>
<line x1="346" y1="64" x2="366" y2="64" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#mt-arw)"/>
<line x1="520" y1="64" x2="540" y2="64" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#mt-arw)"/>
<rect x="20" y="32" width="150" height="64" rx="6" fill="var(--code-bg)" stroke="var(--fg-muted)" stroke-width="1"/>
<text x="95" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Request</text>
<text x="95" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">X-Tenant-ID</text>
<rect x="194" y="32" width="150" height="64" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="269" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Middleware</text>
<text x="269" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">ctx tenant</text>
<rect x="368" y="32" width="150" height="64" rx="6" fill="var(--accent-soft)" stroke="var(--accent)" stroke-width="1"/>
<text x="443" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--accent)">Scoped repo</text>
<text x="443" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">WHERE tenant</text>
<rect x="542" y="32" width="150" height="64" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="617" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Database</text>
<text x="617" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">one tenant's rows</text>
</svg>
<figcaption>Scoping is applied through the repo, so a query only ever sees the current tenant's rows.</figcaption>
</figure>

## Resolving the tenant

Add the middleware, which resolves the tenant and attaches it to the request context. The default resolver reads the `X-Tenant-ID` header; register your own `Resolver` to read a JWT claim or a subdomain instead.

```go
var _ = bosun.Controller[API]("/api", bosun.Use[tenantmod.Middleware]())
```

Any code downstream reads the tenant with `tenantmod.FromContext(ctx)`.

## Scoping a repository

Wrap a repo with a tenant column so every operation is confined to the current tenant: reads and queries filter by the column, creates stamp it, and a get or delete for another tenant's row behaves as not-found. Bind the scoped repo in `main`, naming the concrete driver repo so it wraps the real implementation.

```go
tenantmod.Bind[User, gormrepomod.GormRepo[User]](app.Reg, "tenant_id")
```

A service then injects `repomod.Repo[User]` as usual and never writes a tenant clause by hand.

```go
func (s *Users) List(ctx context.Context) ([]User, error) {
    return s.Repo.Query().Order("name").All(ctx) // implicitly scoped to the tenant
}
```

An operation with no tenant on the context returns `tenantmod.ErrNoTenant`, so a missing tenant fails loudly rather than leaking across tenants.

## Propagation and limits

The tenant rides the event bus through a carrier, exactly like the correlation id, so an event published while handling a tenant's request is consumed under that same tenant. Scoping is advisory: it works because access goes through `repomod.Repo[T]`, so code that reaches a raw `*gorm.DB` bypasses it. For defense in depth against that, add a database-level policy such as Postgres row-level security, and treat the scoped repo as the primary, not the only, guard.
