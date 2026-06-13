# Config & hot reload

Bosun's `config` package lets you bind a JSON key to a typed options
struct and reload it at runtime. Services see changes via
`*bosun.Dynamic[T]` — no restart, no rebuild.

---

## When to use this vs plain env vars

- **Plain env vars / flags at startup** — simplest, fine for things that
  never change at runtime (DB DSN, log level).
- **Bosun config + `Dynamic[T]`** — when an operator should be able to
  change a value (rate limits, feature flags, sampling rates) and have it
  take effect in seconds, without redeploying.

---

## Quick start: file-backed options

### 1. Define the options type

```go
package mw

type RateLimitOptions struct {
    PerMinute int `json:"per_minute"`
}

// Default value if nothing else registers one.
var _ = bosun.DefaultDynamic[RateLimitOptions](func() *RateLimitOptions {
    return &RateLimitOptions{PerMinute: 60}
})
```

### 2. Bind a config key to the type

```go
import "github.com/amberstack/bosun/config"

var _ = config.Bind[mw.RateLimitOptions]("ratelimit")
```

Whenever the watcher sees a change under the JSON key `"ratelimit"`, it
unmarshals into `*RateLimitOptions` and swaps it into the registered
`*bosun.Dynamic[RateLimitOptions]`.

### 3. Add a source

```go
import "github.com/amberstack/bosun/config"

func main() {
    app := bosun.New()
    config.Add(config.FileSource{Path: "config.json"})
    log.Fatal(app.Run(":8080"))
}
```

`config.json`:

```json
{
  "ratelimit": { "per_minute": 120 }
}
```

Edit the file → the watcher picks up the change on its next poll → every
service reading `Opts.Get().PerMinute` sees the new value on the next
request. No restart.

### 4. Read from the service

```go
type RateLimit struct {
    Opts *bosun.Dynamic[RateLimitOptions]   // injected
}

func (m *RateLimit) Handle(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        limit := m.Opts.Get().PerMinute   // freshest value at request time
        ...
    })
}
```

---

## Built-in sources

### `config.FileSource{Path: "..."}`

Reads a JSON document of `{key: object}` from disk. A missing file is OK
(no values); a malformed file is an error.

### `config.EnvSource{}`

Reads environment variables prefixed `BOSUN_CONFIG_<KEY>` (uppercase) and
treats each as a JSON document for that key.

```
BOSUN_CONFIG_RATELIMIT='{"per_minute":200}'
```

### `config.KVSource{Store: ..., Decrypt: ...}`

Reads from a key/value store you provide — typically a database table.
Optional `Decrypt` lets you store sealed bytes and unseal them on read.

```go
config.Add(config.KVSource{
    Store:   configTable,         // any implementation of config.KVStore
    Decrypt: secretBox.Open,
})
```

Use this for runtime configuration that lives in your app's own DB — an
admin endpoint can update rows and the watcher applies them.

---

## Layering: file < env < DB

You can register multiple sources. Later sources override earlier ones
for the same key:

```go
config.Add(config.FileSource{Path: "config.json"})    // baseline
config.Add(config.EnvSource{})                        // ops override
config.Add(config.KVSource{Store: table, Decrypt: ...}) // runtime override
```

Result: defaults come from the file, infra ops can override via env, and
an admin UI can override via a DB row — all live, all without restarts.

---

## Watcher knobs

The watcher polls every source. Default interval is `1s`; override via
the `*Options` value the watcher uses:

```go
registry.RegisterInstance[*config.Options](app.Reg, &config.Options{
    Poll: 500 * time.Millisecond,
})
```

The watcher is itself a `bosun.Service` — it spins a goroutine in
`Init()`. Don't disable the `config` package if you're using
`config.Bind`; nothing will reload.

---

## Reacting to changes

`Dynamic[T]` exposes `OnChange(fn)`:

```go
opts.OnChange(func(v *RateLimitOptions) {
    log.Printf("rate limit changed to %d/min", v.PerMinute)
    // resize pools, reopen connections, etc.
})
```

Register the callback in your service's `Init()`. Bosun calls it on every
`Set` (including the initial load).

---

## Failure semantics

- **Startup** — if any source returns an error or any bind fails to
  unmarshal, `app.Start()` returns the error. Fail fast.
- **Runtime reload** — errors during polling are logged via `slog`; the
  previous good config stays in place. You never get a half-applied
  config.

---

## Writing a custom source

Implement `config.Source`:

```go
type Source interface {
    Load(ctx context.Context) (map[string]json.RawMessage, error)
}
```

Then register either an instance:

```go
config.Add(MyConsulSource{...})
```

Or a registered service with injected deps:

```go
var _ = bosun.Service[ConsulSource]()
var _ = config.AddSource[ConsulSource]()
```

Sources are called on every poll, so make them cheap (cache, etag, etc.)
if the backing store is remote.

---

## Encrypted DB-backed config

For sensitive values, combine `config.SecretBox` (AES-GCM) with `KVSource`:

```go
key := sha256.Sum256([]byte(os.Getenv("CONFIG_MASTER_KEY")))
box, _ := config.NewSecretBox(key[:])

config.Add(config.KVSource{
    Store:   configTable,    // your DB-backed KV store
    Decrypt: box.Open,       // unseal on read
})
```

Your admin endpoint seals plaintext with `box.Seal(...)` and writes the
bytes to the store. The watcher unseals on each poll.

See `examples/kitchen-sink/main.go` for a complete encrypted-config
example wired to a fake DB table.
