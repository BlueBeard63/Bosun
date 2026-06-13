# Service options — defining a value once, injecting it everywhere

When a service has a knob — bcrypt salt rounds, JWT expiry, an API base
URL, a rate limit — you almost never want to thread the value through
constructors or read it from env vars at every call site. Pick one of
three patterns based on how the value should behave at runtime.

## TL;DR — which pattern to use

| Pattern | Define once in… | Hot-reload? | Use when                                 |
| ------- | --------------- | ----------- | ---------------------------------------- |
| **Instance**     | `main`             | No  | Set once at startup, deploy to change (DSN, salt rounds, JWT secret) |
| **Default + override** | The module        | No  | Module ships a sane default; host overrides if needed |
| **Dynamic**            | The module + a source   | Yes | Operator wants to tune live (rate limits, feature flags, sampling) |

For most service knobs, **Instance** or **Default + override**. Use
**Dynamic** only when "no redeploy" is a real requirement.

---

## Pattern 1 — Instance: register once in `main`

The simplest case. You define a typed options struct, register one
instance, and inject it everywhere it's needed.

```go
// hasher/hasher.go
type HasherOptions struct {
    SaltRounds int
}

type Hasher struct {
    Opts *HasherOptions   // injected
}

func (h *Hasher) Hash(pw string) (string, error) {
    return bcrypt.GenerateFromPassword([]byte(pw), h.Opts.SaltRounds)
}

var _ = bosun.Service[Hasher]()
```

```go
// main.go
func main() {
    app := bosun.New()
    registry.RegisterInstance[*HasherOptions](app.Reg, &HasherOptions{
        SaltRounds: 12,
    })
    log.Fatal(app.Run(":8080"))
}
```

Now any service that declares `Opts *HasherOptions` gets the same
singleton. Define once, inject everywhere.

### When to pick this

- The value is set once at startup.
- Changing it means a deploy.
- You don't want the module to assume any default — every host must
  supply it.

### Concrete examples

- bcrypt salt rounds (12 in prod, 4 in tests)
- JWT signing secret (never has a sane default)
- DSN / connection strings
- Third-party API keys

---

## Pattern 2 — Default + override: module ships a sane default

When a module wants to "just work" with reasonable defaults but still let
hosts customize:

```go
// hasher/hasher.go
type HasherOptions struct {
    SaltRounds int
}

// Ship a default — fires only if the host hasn't registered HasherOptions itself.
var _ = bosun.Default[*HasherOptions](func() *HasherOptions {
    return &HasherOptions{SaltRounds: 12}
})

type Hasher struct { Opts *HasherOptions }
func (h *Hasher) Hash(pw string) (string, error) {
    return bcrypt.GenerateFromPassword([]byte(pw), h.Opts.SaltRounds)
}
var _ = bosun.Service[Hasher]()
```

A host that wants the default does nothing:

```go
func main() {
    log.Fatal(bosun.New().Run(":8080"))   // SaltRounds = 12
}
```

A host that wants to override registers before `Start()`:

```go
func main() {
    app := bosun.New()
    registry.RegisterInstance[*HasherOptions](app.Reg, &HasherOptions{
        SaltRounds: 14,
    })
    log.Fatal(app.Run(":8080"))
}
```

The host's registration always wins; the default fires only if nothing
else registered the type.

### When to pick this

- You're shipping a reusable module/package.
- Most consumers can use the default.
- Power users still get an escape hatch.

### Concrete examples

- Default page sizes
- Default cache TTLs
- Default retry counts
- Anything where "the framework's guess" is usually right

---

## Pattern 3 — Dynamic: hot-reloadable

When an operator should be able to change the value at runtime without
redeploying:

```go
// hasher/hasher.go
type HasherOptions struct {
    SaltRounds int `json:"salt_rounds"`
}

// Ship a hot-reloadable default.
var _ = bosun.DefaultDynamic[HasherOptions](func() *HasherOptions {
    return &HasherOptions{SaltRounds: 12}
})

type Hasher struct {
    Opts *bosun.Dynamic[HasherOptions]   // injected
}

func (h *Hasher) Hash(pw string) (string, error) {
    rounds := h.Opts.Get().SaltRounds   // freshest value at call time
    return bcrypt.GenerateFromPassword([]byte(pw), rounds)
}

var _ = bosun.Service[Hasher]()
```

Optionally bind to a config source (file / env / DB) so changes apply
automatically:

```go
// hasher/options.go
var _ = config.Bind[HasherOptions]("hasher")
```

```go
// main.go
func main() {
    app := bosun.New()
    config.Add(config.FileSource{Path: "config.json"})
    log.Fatal(app.Run(":8080"))
}
```

`config.json`:

```json
{
  "hasher": { "salt_rounds": 14 }
}
```

Edit the file → the watcher polls → `Opts.Get().SaltRounds` returns `14`
on the next call. No restart.

### Reacting to changes

```go
func (h *Hasher) Init() error {
    h.Opts.OnChange(func(o *HasherOptions) {
        slog.Info("hasher options changed", "salt_rounds", o.SaltRounds)
        // resize pools, invalidate caches, etc.
    })
    return nil
}
```

### When to pick this

- The value is operationally tuned (rate limits, feature flags).
- You want zero-downtime changes.
- You're OK with "freshest value when called" semantics rather than
  "constant during a request".

### When NOT to pick this

- The cost of changing the value at runtime is high enough that you'd
  rather force a deploy (e.g. bcrypt rounds — bumping live can spike CPU).
- Per-call freshness doesn't add anything (e.g. JWT signing secret —
  you'd rotate via deploy anyway).

For salt rounds specifically, prefer **Pattern 1 or 2** — you usually
want a deploy gate.

---

## Picking the right field type on consumers

The injected type tells the framework what to give you:

| Field type                       | What gets injected                        |
| -------------------------------- | ----------------------------------------- |
| `*HasherOptions`                 | The registered `*HasherOptions` instance  |
| `*bosun.Dynamic[HasherOptions]`  | The registered Dynamic holding `*HasherOptions` |

Pick `*HasherOptions` when you want a frozen value. Pick
`*bosun.Dynamic[HasherOptions]` when you want hot reload. The two are
*not* interchangeable — a service that wants hot reload must inject the
`Dynamic`.

---

## Defining one options struct for several services

If multiple services share a config block, give them the same field:

```go
type AuthOptions struct {
    SaltRounds   int
    TokenLifetime time.Duration
}

type Hasher  struct { Opts *AuthOptions }
type Tokens  struct { Opts *AuthOptions }
type Login   struct { Hasher *Hasher; Tokens *Tokens }

var _ = bosun.Default[*AuthOptions](func() *AuthOptions {
    return &AuthOptions{SaltRounds: 12, TokenLifetime: 24 * time.Hour}
})
```

All three services see the same instance. There is no "config copy" to
keep in sync.

If two services have *different* options that you happen to ship
together, give them separate types — sharing the struct just to bundle
the registration creates a coupling you'll regret later.

---

## A note on env-var-only config

If a knob is set once and only ever read from `os.Getenv` (DSN, log
level), you don't need any of these patterns — just read the env at
service `Init()`:

```go
func (h *Hasher) Init() error {
    h.rounds, _ = strconv.Atoi(os.Getenv("BCRYPT_ROUNDS"))
    if h.rounds == 0 { h.rounds = 12 }
    return nil
}
```

The patterns above are for when you want **typed**, **discoverable**,
**testable** config — which is most of the time, but not always.

---

## Tying it together

The decision really is just three questions:

1. Does the framework ship a default the user can override?
   → If yes, use `bosun.Default[*T]`.
   → If no, registering an instance in `main` is mandatory.

2. Should it hot-reload?
   → If yes, switch to `bosun.DefaultDynamic[T]` + `*bosun.Dynamic[T]` in the consumer.
   → If no, plain `*T` is fine.

3. Should it be driven by a config file / env / DB?
   → If yes, add `config.Bind[T]("key")` and register sources in `main`.
   → If no, your code is the source of truth.

For most service knobs the answer is "default, no hot reload, no config
binding" — i.e. Pattern 2.
