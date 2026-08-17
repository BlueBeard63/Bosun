# Routing internals

Bosun does not implement its own route matcher. It normalizes your path patterns, builds the wrapped handler chain, and registers everything with the Go 1.22 `net/http.ServeMux`, which does the matching. This page explains the two path syntaxes, wildcards, middleware ordering, and how to inspect what got registered.

## Static and dynamic paths

Two path syntaxes are accepted and can be mixed freely. A colon parameter such as `:id` is rewritten internally to the standard library's `{id}` form, so the `path:"id"` binding tag matches either style.

```go
bosun.Get(r, "/users/:id", c.Get)
bosun.Get(r, "/users/{id}", c.Get)                    // equivalent
bosun.Get(r, "/orgs/:org/users/{user_id}", c.Member)  // mixed
```

A static path (no parameters) matches only itself. A dynamic path captures each parameter segment, which you read through the `path:` tag on your request struct or, in a raw handler, through `req.PathValue("id")`.

## Wildcards

A trailing wildcard captures the remainder of the path. Use the standard library's `{name...}` form and bind it like any other parameter.

```go
bosun.Get(r, "/static/{path...}", c.Serve)

type StaticIn struct {
    Path string `path:"path"`
}
```

## Middleware ordering

Middleware runs outermost-first, in the order you declare it, layering from the controller down to the route and finally the handler.

```go
var _ = bosun.Controller[Admin]("/admin",
    bosun.Use[mw.Logging](),      // outermost
    bosun.Use[mw.RequireStaff](),
)

func (c *Admin) Routes(r *bosun.Router) {
    bosun.Post(r, "/wipe", c.Wipe, bosun.Use[mw.DoubleConfirm]())
    // order on /wipe: Logging -> RequireStaff -> DoubleConfirm -> handler
}
```

The raw router methods (`r.Get`) run through the same middleware chain as the typed generics, so authentication and logging behave identically for both.

## Declaring extra error statuses

OpenAPI generation cannot always infer a status code that your handler computes at runtime. Declare such statuses explicitly so they appear in the generated spec.

```go
bosun.Post(r, "/things", c.Create,
    bosun.Errors(http.StatusConflict, http.StatusGone),
)
```

## Inspecting registered routes

After `app.Start()`, every typed route is available through `bosun.TypedRoutes()`. This drives OpenAPI generation, the deploy manifest, and route smoke-tests.

```go
app.Start()
for _, rt := range bosun.TypedRoutes() {
    fmt.Printf("%-6s %s  %s\n", rt.Method, rt.Path, rt.Handler)
}
```
