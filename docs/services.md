# Services

A service is any Go type registered with the framework so its dependencies
get injected automatically and other code can pull it via the registry.
Most app code lives in services: business logic, repositories, clients to
external systems.

---

## Basics

### Declare a service

```go
type UserService struct{}

func (s *UserService) FindByID(id int) (*User, error) { ... }

var _ = bosun.Service[UserService]()
```

That's it. The framework now knows about `*UserService` and will:
- inject `*UserService` into any other service/controller that has a field of that type;
- create exactly one instance, lazily, the first time it's resolved.

### Use it from a controller

```go
type UsersController struct {
    Users *UserService   // injected automatically
}

var _ = bosun.Controller[UsersController]("/users")

func (c *UsersController) Routes(r *bosun.Router) {
    bosun.Get(r, "/:id", c.Get)
}

func (c *UsersController) Get(ctx context.Context, req *bosun.Req[GetIn]) (UserOut, error) {
    u, err := c.Users.FindByID(req.Body.ID)
    if err != nil { return UserOut{}, bosun.E(http.StatusNotFound, "not found", err) }
    return UserOut{Name: u.Name}, nil
}
```

---

## Services depending on services

Fields with registered types are injected. No constructor; no DI annotations
beyond the field's type.

```go
type AuthService struct {
    Users  *UserService     // injected
    Tokens *TokenService    // injected
}

func (a *AuthService) Login(email, pw string) (*Token, error) { ... }

var _ = bosun.Service[AuthService]()
```

If `UserService` is missing from the registry at start time, app startup
returns an error pointing at exactly which dependency couldn't be resolved.

---

## Lifecycle hooks

### `Init() error`

Optional. Runs once, after the service is constructed and its dependencies
are wired:

```go
type UserService struct {
    db    *gorm.DB
    cache map[int]*User
}

func (s *UserService) Init() error {
    s.cache = make(map[int]*User)
    return nil
}
```

Return an error and app startup fails — the cause is propagated up from
`app.Start()`.

### `Close() error` (io.Closer)

Optional. Called by `app.Shutdown()` in reverse dependency order:

```go
func (s *UserService) Close() error {
    return s.db.Close()
}
```

---

## Injecting external instances

Services you don't construct yourself — DB handles, third-party clients,
config — are registered as instances on `app.Reg`:

```go
func main() {
    app := bosun.New()

    db := connectGorm()
    registry.RegisterInstance[*gorm.DB](app.Reg, db)

    redis := connectRedis()
    registry.RegisterInstance[*redis.Client](app.Reg, redis)

    log.Fatal(app.Run(":8080"))
}
```

Any service with a `*gorm.DB` or `*redis.Client` field now gets the live
instance injected.

---

## Skipping injection

A field tagged `inject:"-"` is left zero-valued even if its type is
registered:

```go
type CacheService struct {
    DB    *gorm.DB                  // injected
    local map[string]string `inject:"-"`   // intentionally empty
}
```

Embedded anonymous fields are skipped automatically.

Plain Go types (`int`, `string`, `time.Time`, etc.) are never injected —
they're left zero unless your `Init()` populates them.

---

## Resolving services manually

Sometimes you need to look up a service at runtime — usually only in
plumbing code:

```go
v, err := app.Reg.ResolveType(reflect.TypeOf((*UserService)(nil)))
if err != nil { return err }
users := v.(*UserService)
```

Or via the helper:

```go
users, err := registry.Resolve[*UserService](app.Reg)
```

99% of services should be injected via fields, not looked up by hand.

---

## Advanced

### Defaults: ship a fallback, let the host override

Modules use `Default` to ship an overridable provider. The host's
registration wins; the default fires only if nothing else registered `T` by
the time `app.Start()` runs.

```go
// In your module:
type Options struct{ Greeting string }
var _ = bosun.Default[*Options](func() *Options {
    return &Options{Greeting: "hi"}
})
```

```go
// In the host (optional override):
registry.RegisterInstance[*Options](app.Reg, &Options{Greeting: "yo"})
```

### Binding interfaces to implementations

Code wants to depend on an interface; modules want to ship a concrete type.
`DefaultBind` glues them together unless the host overrides:

```go
type Auditor interface{ Audit(ctx context.Context, ev AuditEvent) }

var _ = bosun.Service[ConsoleAuditor]()
var _ = bosun.DefaultBind[Auditor, ConsoleAuditor]()
```

Any service with an `Auditor` field gets `*ConsoleAuditor` injected unless
the host registered its own `Auditor` first.

### Hot-reloadable values: `*Dynamic[T]`

For config that should apply to live traffic without rebuilding services,
inject `*bosun.Dynamic[T]` and read it per-request:

```go
type RateLimit struct {
    Opts *bosun.Dynamic[RateLimitOptions]
}

func (m *RateLimit) Handle(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        per := m.Opts.Get().PerMinute   // freshest value at request time
        ...
    })
}

var _ = bosun.DefaultDynamic[RateLimitOptions](func() *RateLimitOptions {
    return &RateLimitOptions{PerMinute: 60}
})
```

To swap the value, call `Opts.Set(&newOpts)`. Subscribe to changes with
`Opts.OnChange(fn)`. Pair with the `config` package for file-driven
hot reload (see `config.md`).

### Avoiding cycles

The registry detects cycles at startup. If `A` injects `*B` and `B` injects
`*A`, `app.Start()` returns an error naming the cycle. Break it by
splitting one side into an interface (`B` depends on `Aer interface { ... }`
implemented by `*A`) or by introducing an event channel.

### Validating the graph early

```go
if err := app.Start(); err != nil { log.Fatal(err) }
```

`Start()` walks the entire graph: missing deps, cycles, failed `Init()`
calls all surface here, before traffic hits `Run()`.

### Per-test fixtures

In tests, construct a fresh `App`, register stubs as instances, and call
`Start()`:

```go
func TestUsersGet(t *testing.T) {
    app := bosun.New()
    registry.RegisterInstance[*UserService](app.Reg, &stubUserService{})
    if err := app.Start(); err != nil { t.Fatal(err) }

    rec := httptest.NewRecorder()
    req := httptest.NewRequest("GET", "/users/1", nil)
    app.Mux.ServeHTTP(rec, req)
    // assert on rec
}
```

The instance form always wins over `Service[T]()`'s constructor, which is
exactly what you want for stubs.
