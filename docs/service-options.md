# Service options

When a service has a knob (bcrypt salt rounds, a JWT expiry, an API base URL, a rate limit), you rarely want to thread the value through constructors or read it from an environment variable at every call site. Bosun offers three patterns, and you pick one based on how the value should behave at runtime.

## Choosing a pattern

Use the **instance** pattern when the value is set once at startup and changing it means a deploy, and no default makes sense (a DSN, a JWT secret). Use the **default with override** pattern when a module should work out of the box but let a host customize (a default page size, a cache TTL). Use the **dynamic** pattern only when an operator must change the value on a live server without redeploying (a rate limit, a feature flag). For most knobs, one of the first two is the right answer.

## Instance: register once in main

Define a typed options struct, register a single instance, and inject it wherever it is needed.

```go
type HasherOptions struct {
    SaltRounds int
}

type Hasher struct {
    Opts *HasherOptions // injected
}

func (h *Hasher) Hash(pw string) (string, error) {
    return bcrypt.GenerateFromPassword([]byte(pw), h.Opts.SaltRounds)
}

var _ = bosun.Service[Hasher]()
```

```go
func main() {
    app := bosun.New()
    registry.RegisterInstance[*HasherOptions](app.Reg, &HasherOptions{SaltRounds: 12})
    log.Fatal(app.Run(":8080"))
}
```

Every service that declares `Opts *HasherOptions` now receives the same singleton.

## Default with override: ship a sane default

A module ships a default provider that fires only if the host has not registered the type itself, so consumers get working behavior for free while power users keep an escape hatch.

```go
var _ = bosun.Default[*HasherOptions](func() *HasherOptions {
    return &HasherOptions{SaltRounds: 12}
})
```

A host that wants the default does nothing. A host that wants to change it registers its own instance before `Start()`, and that registration always wins.

```go
registry.RegisterInstance[*HasherOptions](app.Reg, &HasherOptions{SaltRounds: 14})
```

## Dynamic: hot-reloadable

Ship a dynamic default and inject `*bosun.Dynamic[T]`, then read the freshest value per call with `.Get()`.

```go
var _ = bosun.DefaultDynamic[HasherOptions](func() *HasherOptions {
    return &HasherOptions{SaltRounds: 12}
})

type Hasher struct {
    Opts *bosun.Dynamic[HasherOptions] // injected
}

func (h *Hasher) Hash(pw string) (string, error) {
    return bcrypt.GenerateFromPassword([]byte(pw), h.Opts.Get().SaltRounds)
}
```

Bind the options to a config source so edits apply automatically, and react to changes with `OnChange`. The [config guide](./config.md) covers file, environment, and database sources.

```go
var _ = config.Bind[HasherOptions]("hasher")
```

```go
func (h *Hasher) Init() error {
    h.Opts.OnChange(func(o *HasherOptions) {
        slog.Info("hasher options changed", "salt_rounds", o.SaltRounds)
    })
    return nil
}
```

A frozen value and a dynamic value are not interchangeable: a service that wants hot reload must inject the `*bosun.Dynamic[T]`, while a service that wants a constant injects the plain `*T`.

## Sharing one options struct

Several services can share a config block by declaring the same field type, so there is a single instance and no copy to keep in sync. Give services separate option types when their settings are genuinely different, since bundling unrelated settings into one struct just to share a registration creates coupling you will regret.

```go
type AuthOptions struct {
    SaltRounds    int
    TokenLifetime time.Duration
}

type Hasher struct{ Opts *AuthOptions }
type Tokens struct{ Opts *AuthOptions }

var _ = bosun.Default[*AuthOptions](func() *AuthOptions {
    return &AuthOptions{SaltRounds: 12, TokenLifetime: 24 * time.Hour}
})
```

## Deciding quickly

Ask three questions in order. If the framework should ship an overridable default, use `bosun.Default[*T]`; otherwise registering an instance in `main` is mandatory. If the value should hot-reload, switch to `bosun.DefaultDynamic[T]` with a `*bosun.Dynamic[T]` consumer; otherwise a plain `*T` is fine. If it should be driven by a file, environment, or database, add `config.Bind[T]("key")` and register sources in `main`; otherwise your code is the source of truth. For most knobs the answer is a default, no hot reload, and no config binding.
