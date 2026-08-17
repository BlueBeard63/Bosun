# Route groups

A group is a child router that shares a sub-prefix and a middleware stack with its parent. Groups let a controller mount several related routes without repeating the prefix or the middleware on every line. This guide shows how to create groups, how nesting works, and how to use a group purely to share middleware.

<figure class="diagram">
<svg viewBox="0 0 720 170" role="img" aria-labelledby="rg-title rg-desc" xmlns="http://www.w3.org/2000/svg">
<title id="rg-title">Groups accumulate prefix and middleware</title>
<desc id="rg-desc">A controller prefix, a group prefix with its middleware, and a nested group prefix combine into the final route path, with middleware inherited at each level.</desc>
<defs>
<marker id="rg-arw" markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto"><polygon points="0 0, 8 3, 0 6" fill="var(--fg-muted)"/></marker>
</defs>
<line x1="172" y1="64" x2="192" y2="64" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#rg-arw)"/>
<line x1="346" y1="64" x2="366" y2="64" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#rg-arw)"/>
<line x1="520" y1="64" x2="540" y2="64" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#rg-arw)"/>
<rect x="20" y="32" width="150" height="64" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="95" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Controller</text>
<text x="95" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">/admin</text>
<rect x="194" y="32" width="150" height="64" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="269" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Group</text>
<text x="269" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">/staff + auth</text>
<rect x="368" y="32" width="150" height="64" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="443" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Group</text>
<text x="443" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">/v2</text>
<rect x="542" y="32" width="150" height="64" rx="6" fill="var(--accent-soft)" stroke="var(--accent)" stroke-width="1"/>
<text x="617" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--accent)">Route</text>
<text x="617" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">.../v2/metrics</text>
</svg>
<figcaption>Each group joins its prefix onto the parent and appends its middleware; a nested group inherits every ancestor's middleware.</figcaption>
</figure>

## Creating a group

Call `r.Group(prefix, mws...)` inside `Routes` to get a child router. Routes mounted on the child receive the combined prefix and the inherited middleware stack plus whatever the group adds.

```go
func (c *Admin) Routes(r *bosun.Router) {
    bosun.Get(r, "/ping", c.Ping) // /admin/ping

    staff := r.Group("/staff", bosun.Use[mw.RequireStaff]())
    bosun.Get(staff, "/users", c.ListUsers) // /admin/staff/users, with RequireStaff
    bosun.Post(staff, "/wipe", c.Wipe)      // /admin/staff/wipe, with RequireStaff
}
```

Both typed handlers (`bosun.Get(staff, ...)`) and raw handlers (`staff.Get(...)`) work on a group.

## Nesting

Groups nest freely, and a nested group inherits every ancestor's middleware.

```go
staff := r.Group("/staff", bosun.Use[mw.RequireStaff]())
v2 := staff.Group("/v2")
bosun.Get(v2, "/metrics", c.Metrics) // /admin/staff/v2/metrics, still behind RequireStaff
```

Any registration error inside any nested group surfaces from `app.Start()`, because every group shares the parent's error collector.

## Middleware-only groups

Pass an empty prefix to apply middleware to a batch of routes without adding a path segment.

```go
guarded := r.Group("", bosun.Use[mw.RequireAuth]())
bosun.Get(guarded, "/profile", c.Profile)
bosun.Post(guarded, "/settings", c.UpdateSettings)
```

## Path parameters in group prefixes

A group prefix accepts the same `:name` and `{name}` forms as any route, and the parameter binds normally in the child routes.

```go
items := r.Group("/items/:id")
bosun.Get(items, "/show", c.Show) // /admin/items/{id}/show, with path:"id"
```

The two path syntaxes and how they bind are explained in [routing internals](./routing-internals.md).
