# Route groups

A group is a child router that shares a sub-prefix and a middleware stack with its parent. Groups let a controller mount several related routes without repeating the prefix or the middleware on every line. This guide shows how to create groups, how nesting works, and how to use a group purely to share middleware.

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
