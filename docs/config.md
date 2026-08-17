# Config and hot reload

The `config` package binds a JSON key to a typed options struct and reloads it at runtime, so services see changes through `*bosun.Dynamic[T]` with no restart. Reach for it when an operator should be able to change a value (a rate limit, a feature flag, a sampling rate) and have it take effect in seconds. For values that never change at runtime, such as a database DSN or a log level, plain environment variables at startup are simpler.

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
