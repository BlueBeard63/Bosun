# Routing internals

Bosun does not implement its own route matcher. It normalizes your path patterns, builds the wrapped handler chain, and registers everything with the Go 1.22 `net/http.ServeMux`, which does the matching. This page explains the two path syntaxes, wildcards, middleware ordering, and how to inspect what got registered.

<figure class="diagram">
<svg viewBox="0 0 886 250" role="img" aria-labelledby="rl-title rl-desc" xmlns="http://www.w3.org/2000/svg">
<title id="rl-title">Bosun request lifecycle</title>
<desc id="rl-desc">A request flows through the middleware chain, then the typed adapter binds the request body, the handler runs, and a response is written while an audit and observed-status event is emitted.</desc>
<defs>
<marker id="rl-arw" markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto"><polygon points="0 0, 8 3, 0 6" fill="var(--fg-muted)"/></marker>
</defs>
<line x1="172" y1="64" x2="192" y2="64" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#rl-arw)"/>
<line x1="346" y1="64" x2="366" y2="64" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#rl-arw)"/>
<line x1="520" y1="64" x2="540" y2="64" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#rl-arw)"/>
<line x1="694" y1="64" x2="714" y2="64" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#rl-arw)"/>
<line x1="617" y1="96" x2="617" y2="166" stroke="var(--fg-muted)" stroke-width="1" stroke-dasharray="4,3" marker-end="url(#rl-arw)"/>
<text x="628" y="134" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="8" letter-spacing="0.06em" fill="var(--fg-muted)">EMIT</text>
<rect x="20" y="32" width="150" height="64" rx="6" fill="var(--code-bg)" stroke="var(--fg-muted)" stroke-width="1"/>
<text x="95" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">HTTP request</text>
<text x="95" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">GET /users/1</text>
<rect x="194" y="32" width="150" height="64" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="269" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Middleware</text>
<text x="269" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">outermost first</text>
<rect x="368" y="32" width="150" height="64" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="443" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Typed adapter</text>
<text x="443" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">bind In</text>
<rect x="542" y="32" width="150" height="64" rx="6" fill="var(--accent-soft)" stroke="var(--accent)" stroke-width="1"/>
<text x="617" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--accent)">Handler</text>
<text x="617" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">(ctx, Req) (Out, err)</text>
<rect x="716" y="32" width="150" height="64" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="791" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Response</text>
<text x="791" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">JSON + status</text>
<rect x="532" y="166" width="170" height="56" rx="6" fill="var(--code-bg)" stroke="var(--fg-muted)" stroke-width="1" stroke-dasharray="4,3"/>
<text x="617" y="192" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Audit + Observed</text>
<text x="617" y="209" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">redacted snapshot</text>
</svg>
<figcaption>A typed request: the middleware chain wraps the adapter, which binds the body and calls the handler; the handler's result is encoded while an audit event is emitted.</figcaption>
</figure>

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
