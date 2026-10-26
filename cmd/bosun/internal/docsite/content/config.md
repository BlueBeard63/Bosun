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
    if err := app.Run(":8080"); err != nil {
        log.Fatal(err)
    }
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

`config.FileSource` reads a JSON document of `{key: object}` from disk, where a missing file is fine but a malformed one is an error. `config.EnvSource` reads variables prefixed `<KEY>_` and decodes each one into a field of the bound type (covered below). `config.KVSource` reads from a key/value store you provide, typically a database table, with an optional `Decrypt(key, value)` hook to unseal stored bytes on read.

```go
config.Add(config.KVSource{
    Store:   configTable, // any implementation of config.KVGetter
    Decrypt: secretBox.Open,
})
```

## Environment variables

For a bind key such as `"ratelimit"`, `config.EnvSource` collects variables that start with `RATELIMIT_`. The rest of the name selects a field of the bound type. It matches the field's `json` tag or Go name case-insensitively, ignoring underscores, so `RATELIMIT_PER_MINUTE` and `RATELIMIT_PERMINUTE` both set `PerMinute`. A double underscore steps into a nested struct, so `APP_DB__POOL__MAX=10` sets `DB.Pool.Max` under the `"app"` key. Fields of embedded structs are promoted, as in `encoding/json`.

Each value is parsed for its field's type instead of being guessed from the text:

| Field type | Accepted value |
| --- | --- |
| `string` | taken as-is, so `APP_PASSWORD=123456` stays a string |
| `time.Duration` | `time.ParseDuration` syntax, such as `5s` or `1m30s`. A bare `5` is rejected. |
| integers, floats | strict `strconv` parsing. `NaN` and `Inf` are rejected. |
| `bool` | `strconv.ParseBool`: `true`, `false`, `1`, `0` |
| `encoding.TextUnmarshaler` (e.g. `time.Time`) | the raw string |
| slices, maps, structs | a JSON document, e.g. `APP_TAGS=["a","b"]` |

```go
type AppOptions struct {
    Timeout time.Duration
    DB      struct {
        Pool struct {
            Max int `json:"max"`
        } `json:"pool"`
    } `json:"db"`
}

var _ = config.Bind[AppOptions]("app")
// APP_TIMEOUT=5s APP_DB__POOL__MAX=10
```

Variables that name no field are ignored. A value that fails to parse makes the source return an error that names the variable, such as `config: env APP_TIMEOUT: time: missing unit in duration "5"`.

## Layering sources

Register several sources and later ones override earlier ones for the same key. JSON objects are deep-merged, so a later source overrides only the fields it sets: with `{"ratelimit":{"per_minute":100,"burst":10}}` in the file and `RATELIMIT_BURST=20` in the environment, the key resolves to `{"per_minute":100,"burst":20}`. Non-object values, such as arrays, are replaced whole. A common arrangement is a file for baseline defaults, environment variables for an infrastructure override, and a database row for a live admin override, all applied without restarts.

```go
config.Add(config.FileSource{Path: "config.json"})
config.Add(config.EnvSource{})
config.Add(config.KVSource{Store: table, Decrypt: box.Open})
```

## The watcher

The watcher is itself a `bosun.Service` that starts a goroutine in `Init()` and polls every source. The default interval is one second, overridable through a `*config.Options` instance. Do not disable the `config` package while using `config.Bind`, since nothing would reload. The watcher implements `io.Closer`, so `app.Shutdown()` stops the poll goroutine.

When a key disappears from every source, the watcher restores the value the `Dynamic` held before config was first applied, which is normally the `bosun.DefaultDynamic` default.

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

## Validation

Before a value is swapped in, the watcher checks it in two ways.

**Unknown fields.** A field the bound type doesn't declare is an error rather than being silently dropped, so a typo such as `"PerMinite"` can't leave `PerMinute` at zero. Names are matched the way `encoding/json` matches them: by `json` tag or Go field name, case-insensitively, with embedded struct fields promoted. Fields tagged `json:"-"` count as unknown. Each source is checked on its own before the sources are merged, so the error names the field's full path and the source that holds it:

```text
config key "ratelimit": unknown field "PerMinite", "pool.mx" in file config.json
```

Values inside `map[string]any`, `json.RawMessage`, and types with their own `UnmarshalJSON` or `UnmarshalText` aren't inspected. `EnvSource` never produces unknown fields, because variables that name no field are ignored.

**The `Validate` hook.** If the bound type implements `config.Validator`, with either a value or a pointer receiver, the watcher calls `Validate()` on each freshly decoded value and rejects the update when it returns an error:

```go
func (o RateLimitOptions) Validate() error {
    if o.PerMinute <= 0 {
        return errors.New("per_minute must be positive")
    }
    return nil
}
```

The default from `bosun.DefaultDynamic` isn't passed through `Validate`. Rejected values follow the same rules as values that fail to unmarshal, described next.

## Failure semantics

At startup, an error from any source, a failed unmarshal, an unknown field, or a `Validate` error makes `app.Start()` return the error, so the process fails fast. Every key is still attempted, and the error names each key that failed. During a runtime reload, a source error is logged and the whole poll is skipped, so a partial view of the sources is never applied. A key whose value is rejected for any of the other reasons is logged once with its key and keeps its previous value, while every other key still updates.

## Custom sources and encryption

A custom source implements `config.Source` with a single `Load` method, registered either as a ready-made instance with `config.Add` or as a DI-resolved service with `config.AddSource[T]`. Sources are called on every poll, so make a remote-backed source cheap with caching or etags. For sensitive values, combine `config.SecretBox` (AES-GCM) with `KVSource`: an admin endpoint seals plaintext with `box.Seal(key, plaintext)` and writes the bytes under that key, and the watcher unseals them on each poll. A complete encrypted-config example is wired in `examples/kitchen-sink/main.go`.

```go
sealed, err := box.Seal("ratelimit", plaintext)
// UPDATE config SET value = ? WHERE key = 'ratelimit'
config.Add(config.KVSource{Store: table, Decrypt: box.Open})
```

Each value is bound to its config key, which is passed to the AEAD as additional authenticated data. A ciphertext copied from one row to another, for example from `featureflags` to `ratelimit`, fails to decrypt instead of being accepted under the wrong key.

### Migrating values sealed before key binding

Sealed values now start with a version byte (`0x01 || nonce || ciphertext`). Values written by earlier versions (`nonce || ciphertext`, not bound to a key) are rejected by `Open` with an error that says to re-encrypt them. To migrate a table, re-encrypt each row once with `box.Reseal(key, value)` and write the result back. `Reseal` returns values already in the current format unchanged, so it is safe to run more than once. If the application has to keep reading old rows while the migration runs, set `box.AllowLegacy = true` before the box is first used, and remove it once every row has been resealed, since legacy values aren't bound to a key.

```go
for key, value := range rows {
    sealed, err := box.Reseal(key, value)
    if err != nil {
        return fmt.Errorf("resealing %q: %w", key, err)
    }
    table.Put(key, sealed)
}
```
