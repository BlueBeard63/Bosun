# Middleware

Middleware wraps an `http.Handler` to add cross-cutting behavior such as logging, authentication, or rate limiting. In Bosun a middleware is a registered type with optional injected dependencies, referenced at a route with `bosun.Use[T]()`. This guide covers writing middleware, attaching it, passing typed values to handlers, and the built-ins. The full authentication-and-permissions chain has its own page: [auth and permissions](./auth-and-permissions.md).

<figure class="diagram">
<svg viewBox="0 0 620 300" role="img" aria-labelledby="mw-title mw-desc" xmlns="http://www.w3.org/2000/svg">
<title id="mw-title">Middleware wraps the handler</title>
<desc id="mw-desc">Controller middleware wraps route middleware, which wraps the typed adapter, which wraps the handler. The outermost middleware runs first on the way in and last on the way out.</desc>
<defs>
<marker id="mw-arw" markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto"><polygon points="0 0, 8 3, 0 6" fill="var(--fg-muted)"/></marker>
</defs>
<line x1="6" y1="150" x2="36" y2="150" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#mw-arw)"/>
<text x="4" y="142" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="8" letter-spacing="0.06em" fill="var(--fg-muted)">REQUEST</text>
<rect x="40" y="28" width="544" height="240" rx="8" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="56" y="49" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="8" letter-spacing="0.12em" fill="var(--fg-muted)">CONTROLLER MIDDLEWARE</text>
<rect x="92" y="64" width="440" height="168" rx="8" fill="var(--code-bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="108" y="85" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="8" letter-spacing="0.12em" fill="var(--fg-muted)">ROUTE MIDDLEWARE</text>
<rect x="144" y="100" width="336" height="96" rx="8" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="160" y="121" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="8" letter-spacing="0.12em" fill="var(--fg-muted)">TYPED ADAPTER</text>
<rect x="250" y="136" width="124" height="48" rx="6" fill="var(--accent-soft)" stroke="var(--accent)" stroke-width="1"/>
<text x="312" y="164" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--accent)">Handler</text>
</svg>
<figcaption>Each layer wraps the next. On the way in, the outermost middleware runs first; on the way out, it runs last.</figcaption>
</figure>

## Writing middleware

A middleware is any type that implements `Handle(next http.Handler) http.Handler`.

```go
type Logging struct{}

func (m *Logging) Handle(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        log.Printf("%s %s", r.Method, r.URL.Path)
        next.ServeHTTP(w, r)
    })
}

var _ = bosun.Middleware[Logging]()
```

`bosun.Middleware[T]()` is `bosun.Service[T]()` plus a compile-time check that `T` satisfies the middleware contract; if it does not, registration panics with a clear message at startup.

## Attaching middleware

Attach middleware controller-wide by passing it after the prefix, or per-route by appending it to the handler registration.

```go
var _ = bosun.Controller[UsersController]("/users", bosun.Use[Logging]())

func (c *UsersController) Routes(r *bosun.Router) {
    bosun.Post(r, "/login", c.Login, bosun.Use[RateLimit]())
}
```

The chain runs outermost-first: controller-wide middleware, then per-route middleware, then the handler. Ordering is covered in [routing internals](./routing-internals.md).

## App-wide middleware

Some concerns, such as panic recovery, correlation IDs, request logging, CORS, security headers and tracing, apply to every route. Register them once with `bosun.WithMiddleware` instead of repeating them on every controller.

```go
app := bosun.New(bosun.WithMiddleware(
    bosun.Use[mw.Correlation](),
    bosun.Use[mw.Logging](),
))
```

`WithMiddleware` accepts the same references as routes: `bosun.Use[T]()`, `bosun.Use[T](args...)` and `bosun.UseFunc(f)`. If you pass the option more than once, the later middleware is added to the end of the list. Errors such as a `Configure` argument mismatch are returned from `app.Start()`.

App-wide middleware wraps the whole `ServeMux`, not individual routes. As a result:

- It runs for every request, including framework endpoints such as `/openapi.json` and the manifest, unmatched paths (404/405), and `OPTIONS` preflight requests that no controller route matches.
- It runs before routing, so `r.PathValue(...)` is not populated yet.
- The full order is **app, then controller, then group, then route, then the typed adapter, then the handler**. Within each list, the first entry is outermost.

App-wide middleware lives on the app's root handler. Serve the app through `app.Run`/`RunContext`/`Serve`, `app.Handler()` or `app` itself, which implements `http.Handler`. Serving `app.Mux` directly bypasses app-wide middleware.

```go
srv := &http.Server{Addr: ":8080", Handler: app.Handler()}
```

### A production stack

A typical ordering puts the cheapest and broadest concerns outermost, with panic recovery just inside the observability layers:

```go
app := bosun.New(bosun.WithMiddleware(
    bosun.Use[mw.RealIP](realIPOpts),   // client IP behind your load balancer
    bosun.Use[mw.Correlation](),        // id available to every later layer
    bosun.Use[mw.Logging](),            // logs every request, including 404s and recovered 500s
    bosun.Use[tracemod.Tracing](),      // optional: span marked as errored on 500
    bosun.Use[mw.SecureHeaders](),      // security headers, also on recovered 500s
    bosun.Use[mw.Compress](mw.CompressOptions{}),
    bosun.Use[mw.Recover](),            // everything below is panic-safe
    bosun.Use[mw.CORS](corsOpts),       // answers preflights before routing
    bosun.Use[mw.ETag](mw.ETagOptions{}),
))
```

`Timeout` is usually better attached to the controllers or groups that need it than app-wide, because it ends streaming responses. See [Timeouts](#timeouts).

Authentication and permissions usually belong on controllers or groups rather than at the app level, because some routes, such as health checks and login, must stay public.

## Middleware with dependencies

Because middleware is a service, it can inject anything the registry knows about, and it may implement `Init()` and `Close()` just like any other service.

```go
type RequireAuth struct {
    Auth *AuthService // injected
}

func (m *RequireAuth) Handle(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if !m.Auth.Valid(r.Header.Get("Authorization")) {
            http.Error(w, "forbidden", http.StatusForbidden)
            return
        }
        next.ServeHTTP(w, r)
    })
}

var _ = bosun.Middleware[RequireAuth]()
```

## Passing typed values to handlers

Middleware communicates with downstream handlers through typed context values. The key is the Go type itself, so there are no string keys and no type assertions. A middleware attaches a value with `bosun.WithValue`, and a handler reads it with `bosun.Value`.

```go
type AuthUser struct {
    ID   int
    Name string
}

func (m *RequireAuth) Handle(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        u, err := m.Sessions.Lookup(r.Header.Get("Authorization"))
        if err != nil {
            http.Error(w, "forbidden", http.StatusForbidden)
            return
        }
        next.ServeHTTP(w, r.WithContext(bosun.WithValue(r.Context(), u))) // u is *AuthUser
    })
}
```

```go
func (c *Me) Get(ctx context.Context, _ *bosun.Req[struct{}]) (UserOut, error) {
    u := bosun.Value[AuthUser](ctx)
    if u == nil {
        return UserOut{}, bosun.E(http.StatusUnauthorized, "not signed in", nil)
    }
    return UserOut{Name: u.Name}, nil
}
```

Because each type gets its own slot, `bosun.Value[AuthUser]` and `bosun.Value[Org]` never collide, and middleware can stack several values that handlers read independently.

## Built-in middleware

The `mw` package ships ready-to-use middleware.

```go
import "github.com/bluebeard63/bosun/mw"

bosun.Use[mw.Logging]()          // slog-based request logger
bosun.Use[mw.RateLimit]()        // per-client limit, hot-reloadable
bosun.Use[mw.Correlation]()      // attaches a correlation id to every request
bosun.Use[mw.Recover]()          // turns panics into a logged JSON 500
bosun.Use[mw.CORS](opts)         // cross-origin requests and preflights
bosun.Use[mw.RealIP](opts)       // client IP from trusted proxies' headers
bosun.Use[mw.Compress](opts)     // gzip/deflate responses
bosun.Use[mw.Timeout](opts)      // per-request deadline, 503 on expiry
bosun.Use[mw.SecureHeaders]()    // HSTS, frame options, nosniff, ...
bosun.Use[mw.ETag](opts)         // ETag + 304 Not Modified
```

`Correlation` is described in the [tracing guide](./tracing.md). `Recover` is described under [panics](./errors.md#panics).

Every built-in that takes options works both ways: `bosun.Use[mw.X](opts)` fixes the options for that attachment point, and `bosun.Use[mw.X]()` reads an injected `*bosun.Dynamic[mw.XOptions]` on every request, so you can change it at runtime through the [config module](./config.md) or by calling `Set`. Each one forwards `Flush`, `Hijack` and `Unwrap` on the `ResponseWriter` it wraps, so server-sent events, WebSockets and `http.ResponseController` keep working behind it.

## Request logging

`mw.Logging` writes one `slog` line per request at Info level, through `InfoContext` so a context-aware `slog.Handler` sees the request context. Each line carries:

| Attribute | Value |
|---|---|
| `method`, `path`, `status` | From the request and the response. |
| `duration` | Time spent in the rest of the chain, such as `1.2ms`. |
| `remote` | `r.RemoteAddr`, the TCP peer. Behind a proxy this is the proxy. |
| `correlation_id` | The id set by `mw.Correlation`, so log lines join up with `Recover`'s panic logs and audit events. |
| `client_ip` | Only present when `mw.RealIP` ran upstream: the resolved client address. |

Register `Correlation` (and `RealIP`) outside `Logging` so their values are on the context `Logging` sees. If `Correlation` runs inside `Logging`, the id is read from the `X-Correlation-ID` response header instead.

## Real client IP behind proxies

Behind a load balancer or CDN, `r.RemoteAddr` is the proxy, not the client. `mw.RealIP` recovers the client address from forwarding headers, but only believes them when the request arrived from a proxy you trust.

```go
app := bosun.New(bosun.WithMiddleware(
    bosun.Use[mw.RealIP](mw.RealIPOptions{
        TrustedProxies: []string{"10.0.0.0/8", "fd00::/8", "127.0.0.1"},
    }),
    bosun.Use[mw.Correlation](),
    bosun.Use[mw.Logging](),
))
```

How the client IP is resolved:

1. If the TCP peer (`r.RemoteAddr`) is not in `TrustedProxies`, the forwarding headers are ignored and the peer is the client. Anyone can send `X-Forwarded-For`, so it is only meaningful when your proxy wrote it.
2. Otherwise RealIP reads `X-Forwarded-For`, or, if that is absent, the `for=` values of `Forwarded` (RFC 7239), or, if both are absent, `X-Real-IP`.
3. The `X-Forwarded-For` and `Forwarded` chains are walked from right to left, skipping addresses that are themselves trusted proxies. The first untrusted address is the client. Anything to its left was supplied by the client and is never believed. If every hop is trusted, the leftmost is used. A malformed hop stops the walk at the proxy that reported it.

Entries in `TrustedProxies` are CIDRs or single IPs, IPv4 or IPv6. IPv4-mapped IPv6 addresses match their IPv4 form. An invalid entry makes `Configure` panic at startup. An invalid list loaded at runtime through `*bosun.Dynamic[mw.RealIPOptions]` trusts no proxy. The zero value trusts no proxy.

RealIP stores the result on the request context and leaves `r.RemoteAddr` untouched, so the TCP peer is still available. Read the client IP with `mw.ClientIP(r)`. It returns the address RealIP resolved, or the host part of `r.RemoteAddr` when RealIP did not run.

```go
func (m *AuditIP) Handle(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        slog.InfoContext(r.Context(), "hit", "ip", mw.ClientIP(r))
        next.ServeHTTP(w, r)
    })
}
```

Register RealIP outermost, so `Logging`, `RateLimit` and your own middleware all see the resolved address.

## Rate limiting

`mw.RateLimit` is a fixed-window limiter with one window per client per minute. It keys on `mw.ClientIP(r)`, so **behind a load balancer you must register `mw.RealIP`** with the balancer's addresses. Without it every client shares the balancer's bucket.

The limit comes from `*bosun.Dynamic[mw.RateLimitOptions]` (default 60 per minute) and can be tuned at runtime through the [config module](./config.md). Every response carries `X-RateLimit-Limit` and `X-RateLimit-Remaining`. A rejected request gets `429 Too Many Requests` with `Retry-After` set to the whole seconds until the client's window resets.

Windows live in process memory. For several replicas, replace `RateLimit` with an implementation backed by a shared store.

## Compression

`mw.Compress` compresses responses with gzip, or with deflate when the client prefers it, based on `Accept-Encoding` and its q-values. It uses only the Go standard library, so Brotli (`br`) and zstd are **not** supported. A client that only accepts those gets an uncompressed response.

```go
bosun.Use[mw.Compress](mw.CompressOptions{
    Level:   gzip.BestSpeed,                  // 1-9; 0 = default (6)
    MinSize: 512,                             // bytes; 0 = 1024
    ContentTypes: []string{"text/*", "application/json", "*+json"},
})
```

| Option | Default | Notes |
|---|---|---|
| `Level` | `0` (library default, 6) | 1 (fastest) to 9 (smallest), or -2 for Huffman-only. Other values panic at startup. |
| `MinSize` | `1024` | Smaller bodies are sent as they are. |
| `ContentTypes` | `mw.DefaultCompressTypes` | Exact types, `type/*` wildcards or `*+suffix` wildcards. The defaults cover text, JSON, XML, JavaScript, SVG and WebAssembly. |

A response is left uncompressed when:

- the client accepts neither gzip nor deflate;
- the status is 1xx, 204, 206 or 304;
- the handler already set `Content-Encoding`, set `Content-Range`, or sent `Cache-Control: no-transform`;
- the `Content-Type` is not in `ContentTypes`. If the handler set no `Content-Type`, it is sniffed from the body, as `net/http` would;
- the body is smaller than `MinSize`. Compress buffers up to `MinSize` bytes to decide, unless the handler declared a `Content-Length`.

When it compresses, Compress sets `Content-Encoding`, removes `Content-Length` and `Accept-Ranges`, and turns a strong `ETag` into a weak one, because the bytes differ from the uncompressed representation. Every response gets `Vary: Accept-Encoding`, added alongside any `Vary` the handler set.

Streaming works. When the handler flushes, Compress flushes the compressor and then the connection, so each server-sent event reaches the client immediately. Flushing commits the decision, so a compressible stream is compressed whatever its size so far. Hijacked connections (WebSockets) pass through untouched.

Register Compress outside `Recover`. A panic then still produces `Recover`'s plain JSON 500. It is too small to compress, and no `Content-Encoding` header is left on it. If Compress sits inside `Recover`, any output it was still buffering is discarded and the 500 goes straight to the client.

## Timeouts

`mw.Timeout` gives each request a deadline. When the deadline passes before the handler has started its response, the client gets `503 Service Unavailable` with `{"error":"request timed out"}`:

```go
var _ = bosun.Controller[ReportsController]("/reports",
    bosun.Use[mw.Timeout](mw.TimeoutOptions{Duration: 5 * time.Second}),
)
```

`Duration` defaults to 30 seconds. A negative value disables the middleware, which is useful to switch it off at runtime through `*bosun.Dynamic[mw.TimeoutOptions]`.

How it behaves:

- The request context carries the deadline. Database drivers, HTTP clients and typed handlers that pass `ctx` along stop when it expires.
- If the response has not started when the deadline passes, the handler's later output is discarded: `Write` returns `http.ErrHandlerTimeout`, and the 503 is sent instead. Headers the handler set are dropped, except `Vary`, `Access-Control-*` and the correlation id, the same list `Recover` keeps.
- If the response has already started, its status is on the wire and is left alone. The context is still cancelled.
- A client that disconnects cancels the context, but does not cause a 503.

Unlike `http.TimeoutHandler`, `mw.Timeout` does not run the handler on a separate goroutine. Nothing writes to the `ResponseWriter` concurrently, the response is not buffered, and `Flush`, `Hijack` and `http.ResponseController` keep working. The trade-off is that the deadline is **cooperative**. A handler that ignores `ctx` runs to completion, and only then is its late response replaced by the 503. The server's own `ReadTimeout` and `WriteTimeout` remain the backstop for code that never checks `ctx`.

Don't put streaming endpoints (server-sent events, WebSockets, long polling) behind `Timeout`, because the cancelled context ends the stream. This is why it is usually attached per controller or group rather than app-wide.

## Security headers

`mw.SecureHeaders` adds the usual browser security headers:

```go
bosun.Use[mw.SecureHeaders]()  // defaults, hot-reloadable

opts := mw.DefaultSecureHeaders()
opts.ContentSecurityPolicy = "default-src 'none'; frame-ancestors 'none'"
opts.HSTSIncludeSubdomains = true
bosun.Use[mw.SecureHeaders](opts)  // fixed policy
```

| Option | `DefaultSecureHeaders()` | Header |
|---|---|---|
| (always) | — | `X-Content-Type-Options: nosniff` |
| `HSTSMaxAge`, `HSTSIncludeSubdomains`, `HSTSPreload` | 1 year, no subdomains, no preload | `Strict-Transport-Security` |
| `FrameOptions` | `DENY` | `X-Frame-Options` |
| `ReferrerPolicy` | `strict-origin-when-cross-origin` | `Referrer-Policy` |
| `CrossOriginOpenerPolicy` | `same-origin` | `Cross-Origin-Opener-Policy` |
| `ContentSecurityPolicy` | unset | `Content-Security-Policy` |
| `PermissionsPolicy` | unset | `Permissions-Policy` |

Empty string fields and a zero `HSTSMaxAge` omit their header, so build options from `mw.DefaultSecureHeaders()` rather than from the zero value. Browsers ignore HSTS on plain-HTTP responses, so it is safe to send behind a TLS-terminating proxy and during local development. A Content-Security-Policy depends on what your pages load, so none is set by default. For a JSON-only API, `default-src 'none'; frame-ancestors 'none'` is a good choice.

The headers are applied when the response is committed, and only where the handler has not set the header itself, so a handler can override any of them for one response. Because they are applied that late, they also appear on `Recover`'s 500 when SecureHeaders is registered outside `Recover`.

## ETags and conditional requests

`mw.ETag` gives successful `GET` and `HEAD` responses an `ETag` derived from a SHA-256 hash of the body. It answers a matching `If-None-Match` with `304 Not Modified` and no body, so clients and caches can revalidate without downloading the body again. Handlers need no changes. The handler still runs, so ETag saves bandwidth, not work.

```go
bosun.Use[mw.ETag](mw.ETagOptions{
    MaxBodySize: 256 << 10, // bytes; 0 = 1 MiB
    Weak:        false,     // true emits W/"..." validators
})
```

To hash the body, ETag buffers it. It steps aside, streaming the response untouched and without an ETag, whenever buffering would be wrong or wasteful:

- the method is not `GET` or `HEAD`, or the status is not 200;
- the handler set its own `ETag`, for example through `http.ServeContent`. That ETag is kept;
- the body grows past `MaxBodySize`;
- the handler flushes (server-sent events, long polling) or hijacks the connection;
- a `HEAD` handler writes no body, so there is nothing to hash.

`If-None-Match` uses weak comparison, so `W/"x"` matches `"x"`, and `*` matches any response. Place ETag inside `Compress`. The hash then covers the uncompressed body, Compress weakens the tag on compressed responses, and a client revalidating with that weak tag still gets its 304.

## CORS

`mw.CORS` lets browser frontends on another origin call your API. It answers preflight requests (an `OPTIONS` request carrying `Access-Control-Request-Method`) itself with `204 No Content`, and adds `Access-Control-*` headers to actual requests from allowed origins.

Register it **app-wide**. Controller routes are registered for specific methods such as `GET` and `POST`, so the `ServeMux` never sends an `OPTIONS` preflight to controller or route middleware. Only middleware that wraps the whole app sees it.

A typical setup has a frontend dev server on `http://localhost:5173` and an API on `:8080`:

```go
app := bosun.New(bosun.WithMiddleware(
    bosun.Use[mw.Recover](),
    bosun.Use[mw.CORS](mw.CORSOptions{
        AllowedOrigins:   []string{"http://localhost:5173", "https://app.example.com"},
        AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE"},
        AllowedHeaders:   []string{"Content-Type", "Authorization"},
        ExposedHeaders:   []string{"X-Correlation-ID"},
        AllowCredentials: true,             // cookies / Authorization from the browser
        MaxAge:           10 * time.Minute, // cache preflight results
    }),
))
```

```js
// frontend at http://localhost:5173
await fetch("http://localhost:8080/api/items", {
  method: "POST",
  credentials: "include",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ name: "widget" }),
});
```

| Option | Default | Notes |
|---|---|---|
| `AllowedOrigins` | none | Exact origins; `"*"` for any; one wildcard subdomain such as `"https://*.example.com"`. Case-insensitive. |
| `AllowOriginFunc` | nil | Called for origins that the list doesn't match. |
| `AllowedMethods` | `GET, HEAD, POST, PUT, PATCH, DELETE` | Checked against `Access-Control-Request-Method`. |
| `AllowedHeaders` | `Accept, Authorization, Content-Type, X-Correlation-ID` | `"*"` allows any requested header. |
| `ExposedHeaders` | none | Response headers that scripts may read. |
| `AllowCredentials` | `false` | When true, the request origin is echoed back and `*` is never sent. Combining it with the `*` origin panics at startup; list the trusted origins instead. |
| `MaxAge` | `0` (header omitted) | How long a preflight result may be cached, in whole seconds. |

How it behaves:

- The zero value allows no origins. Cross-origin access is opt-in.
- A request without an `Origin` header (same-origin traffic, curl, server-to-server) passes through unchanged.
- A disallowed origin, method or header gets no CORS headers, so the browser blocks the response. A disallowed preflight still gets a `204`, but without CORS headers.
- `Vary: Origin` is always set, so caches keep different origins apart.

To change the policy at runtime, use `bosun.Use[mw.CORS]()` with no arguments. The middleware then reads an injected `*bosun.Dynamic[mw.CORSOptions]` on every request, so you can update the policy through the [config module](./config.md) or by calling `Set` yourself. The default policy allows no origins.

## Short-circuiting a request

To reject a request, write the response and return without calling `next.ServeHTTP`.

```go
if !m.allow(r) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusForbidden)
    json.NewEncoder(w).Encode(map[string]string{"error": "forbidden"})
    return
}
next.ServeHTTP(w, r)
```

A short-circuited response does not emit an audit event, because the typed adapter only runs when the request reaches the handler. To record denials, log them from the middleware itself.

## Recording the response status

To observe the status code, wrap the `ResponseWriter`. This is exactly how `mw.Logging` is implemented.

```go
type statusRec struct {
    http.ResponseWriter
    status int
}

func (r *statusRec) WriteHeader(c int) { r.status = c; r.ResponseWriter.WriteHeader(c) }

func (m *Logging) Handle(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        rec := &statusRec{ResponseWriter: w, status: 200}
        next.ServeHTTP(rec, r)
        log.Printf("status=%d", rec.status)
    })
}
```

## Per-request versus per-mount work

`Handle(next)` is called once when the route is mounted, not per request. The `http.HandlerFunc` it returns is what runs for each request, so anything request-scoped (timers, recorders) must be created inside that returned function.

```go
func (m *Logging) Handle(next http.Handler) http.Handler {
    // runs once at mount time
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        start := time.Now() // runs per request
        next.ServeHTTP(w, r)
        m.log.Info("done", "ms", time.Since(start).Milliseconds())
    })
}
```

## Hot-reloadable configuration

Inject a `*bosun.Dynamic[Options]` and read it per request so configuration changes apply to live traffic. This pattern is described in the [services guide](./services.md).

```go
type RateLimit struct {
    Opts *bosun.Dynamic[Options]
}

func (m *RateLimit) Handle(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        limit := m.Opts.Get().PerMinute // fresh every request
        ...
    })
}
```
