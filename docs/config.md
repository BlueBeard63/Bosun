# Config and hot reload

The `config` package binds a JSON key to a typed options struct and reloads it at runtime, so services see changes through `*bosun.Dynamic[T]` with no restart. Reach for it when an operator should be able to change a value (a rate limit, a feature flag, a sampling rate) and have it take effect in seconds. For values that never change at runtime, such as a database DSN or a log level, plain environment variables at startup are simpler.

<figure class="diagram">
<svg viewBox="0 0 720 170" role="img" aria-labelledby="cfg-title cfg-desc" xmlns="http://www.w3.org/2000/svg">
<title id="cfg-title">Config hot-reload flow</title>
<desc id="cfg-desc">Layered sources are polled by the watcher, which swaps a new value into a Dynamic holder that services read per request.</desc>
<defs>
<marker id="cfg-arw" markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto"><polygon points="0 0, 8 3, 0 6" fill="var(--fg-muted)"/></marker>
</defs>
<line x1="172" y1="64" x2="192" y2="64" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#cfg-arw)"/>
<line x1="346" y1="64" x2="366" y2="64" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#cfg-arw)"/>
<line x1="520" y1="64" x2="540" y2="64" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#cfg-arw)"/>
<rect x="20" y="32" width="150" height="64" rx="6" fill="var(--code-bg)" stroke="var(--fg-muted)" stroke-width="1"/>
<text x="95" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Sources</text>
<text x="95" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">file &lt; env &lt; db</text>
<rect x="194" y="32" width="150" height="64" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="269" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Watcher</text>
<text x="269" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">polls each source</text>
<rect x="368" y="32" width="150" height="64" rx="6" fill="var(--accent-soft)" stroke="var(--accent)" stroke-width="1"/>
<text x="443" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--accent)">Dynamic[T]</text>
<text x="443" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">atomic swap</text>
<rect x="542" y="32" width="150" height="64" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="617" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Service</text>
<text x="617" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">.Get() per request</text>
</svg>
<figcaption>Later sources override earlier ones for the same key; the watcher swaps the new value in, and the next .Get() returns it with no restart.</figcaption>
</figure>

## File-backed options

Define the options type with a dynamic default, bind a config key to it, and add a source. When the watcher sees a change under the bound key, it unmarshals the value and swaps it into the registered `*bosun.Dynamic[T]`.

```go
type RateLimitOptions struct {
    PerMinute int `json:"per_minute"`
}

var _ = bosun.DefaultDynamic[RateLimitOptions](func() *RateLimitOptions {
    return &RateLimitOptions{PerMinute: 60}
})

var _ = config.Bind[RateLimitOptions]("ratelimit")
```

```go
func main() {
    app := bosun.New()
    config.Add(config.FileSource{Path: "config.json"})
    log.Fatal(app.Run(":8080"))
}
```

Editing `config.json` under the `"ratelimit"` key makes every service reading `Opts.Get().PerMinute` observe the new value on its next request.

```go
type RateLimit struct {
    Opts *bosun.Dynamic[RateLimitOptions] // injected
}

func (m *RateLimit) Handle(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        limit := m.Opts.Get().PerMinute // freshest value at request time
        ...
    })
}
```

## Built-in sources

`config.FileSource` reads a JSON document of `{key: object}` from disk, where a missing file is fine but a malformed one is an error. `config.EnvSource` reads variables prefixed `BOSUN_CONFIG_<KEY>`, each holding a JSON document for that key. `config.KVSource` reads from a key/value store you provide, typically a database table, with an optional `Decrypt` hook to unseal stored bytes on read.

```go
config.Add(config.KVSource{
    Store:   configTable, // any implementation of config.KVStore
    Decrypt: secretBox.Open,
})
```

## Layering sources

Register several sources and later ones override earlier ones for the same key. A common arrangement is a file for baseline defaults, environment variables for an infrastructure override, and a database row for a live admin override, all applied without restarts.

```go
config.Add(config.FileSource{Path: "config.json"})
config.Add(config.EnvSource{})
config.Add(config.KVSource{Store: table, Decrypt: box.Open})
```

## The watcher

The watcher is itself a `bosun.Service` that starts a goroutine in `Init()` and polls every source. The default interval is one second, overridable through a `*config.Options` instance. Do not disable the `config` package while using `config.Bind`, since nothing would reload.

```go
registry.RegisterInstance[*config.Options](app.Reg, &config.Options{Poll: 500 * time.Millisecond})
```

## Reacting to changes

Register an `OnChange` callback in a service's `Init()`. Bosun invokes it on every `Set`, including the initial load, which is the place to resize pools or reopen connections.

```go
opts.OnChange(func(v *RateLimitOptions) {
    log.Printf("rate limit changed to %d/min", v.PerMinute)
})
```

## Failure semantics

At startup, an error from any source or a failed unmarshal makes `app.Start()` return the error, so the process fails fast. During a runtime reload, polling errors are logged and the previous good config stays in place, so you never get a half-applied configuration.

## Custom sources and encryption

A custom source implements `config.Source` with a single `Load` method, registered either as a ready-made instance with `config.Add` or as a DI-resolved service with `config.AddSource[T]`. Sources are called on every poll, so make a remote-backed source cheap with caching or etags. For sensitive values, combine `config.SecretBox` (AES-GCM) with `KVSource`: an admin endpoint seals plaintext with `box.Seal` and writes the bytes, and the watcher unseals them on each poll. A complete encrypted-config example is wired in `examples/kitchen-sink/main.go`.
