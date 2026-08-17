# Services

A service is any Go type you register with Bosun so that its dependencies are injected automatically and other code can depend on it. Most of your application lives in services: business logic, repositories, and clients to external systems. This guide covers how to declare services, how injection and lifecycle work, and the advanced patterns for defaults, interfaces, and hot reload.

## Declaring a service

Register a type with `bosun.Service` and the framework takes over its construction.

```go
type UserService struct{}

func (s *UserService) FindByID(id int) (*User, error) { ... }

var _ = bosun.Service[UserService]()
```

From now on the framework knows about `*UserService`. It injects that pointer into any service or controller that has a field of the type, and it creates exactly one instance, lazily, the first time the instance is resolved.

## Using a service from a controller

Add a field of the service's pointer type to your controller. The field is populated for you.

```go
type UsersController struct {
    Users *UserService // injected automatically
}

var _ = bosun.Controller[UsersController]("/users")

func (c *UsersController) Routes(r *bosun.Router) {
    bosun.Get(r, "/:id", c.Get)
}

func (c *UsersController) Get(ctx context.Context, req *bosun.Req[GetIn]) (UserOut, error) {
    u, err := c.Users.FindByID(req.Body.ID)
    if err != nil {
        return UserOut{}, bosun.E(http.StatusNotFound, "not found", err)
    }
    return UserOut{Name: u.Name}, nil
}
```

## Services depending on services

Any field whose type is registered is injected. There is no constructor and no annotation beyond the field's type.

```go
type AuthService struct {
    Users  *UserService  // injected
    Tokens *TokenService // injected
}

func (a *AuthService) Login(email, pw string) (*Token, error) { ... }

var _ = bosun.Service[AuthService]()
```

If `UserService` is not registered when the app starts, startup fails with an error that names exactly which dependency could not be resolved.

## Lifecycle hooks

A service may implement two optional hooks.

`Init() error` runs once, after the service is constructed and its dependencies are wired. Use it to set up state that does not belong in a field. Returning an error fails startup, and the cause is propagated from `app.Start()`.

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

`Close() error` satisfies `io.Closer` and is called by `app.Shutdown()` in reverse dependency order, so a service is always torn down before the things it depends on.

```go
func (s *UserService) Close() error {
    return s.db.Close()
}
```

## Injecting external instances

Values you construct yourself (database handles, third-party clients, configuration) are registered as instances on `app.Reg` before the app starts.

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

Any service with a `*gorm.DB` or `*redis.Client` field now receives the live instance.

## Skipping injection

A field tagged `inject:"-"` is left at its zero value even if its type is registered. Embedded anonymous fields are skipped automatically, and plain Go types such as `int`, `string`, and `time.Time` are never injected; populate those in `Init()`.

```go
type CacheService struct {
    DB    *gorm.DB          // injected
    local map[string]string `inject:"-"` // intentionally empty
}
```

## Resolving a service by hand

Injection through fields covers almost every case. When plumbing code needs to look up a service at runtime, use the registry helper.

```go
users, err := registry.Resolve[*UserService](app.Reg)
```

## Shipping an overridable default

Modules use `bosun.Default` to ship a provider that fires only if the host has not registered the type itself by the time `app.Start()` runs. The host's registration always wins.

```go
// In your module:
type Options struct{ Greeting string }

var _ = bosun.Default[*Options](func() *Options {
    return &Options{Greeting: "hi"}
})
```

```go
// In the host, to override:
registry.RegisterInstance[*Options](app.Reg, &Options{Greeting: "yo"})
```

## Binding an interface to an implementation

When callers should depend on an interface but a module ships a concrete type, `bosun.DefaultBind` connects the two unless the host overrides the binding.

```go
type Auditor interface{ Audit(ctx context.Context, ev AuditEvent) }

var _ = bosun.Service[ConsoleAuditor]()
var _ = bosun.DefaultBind[Auditor, ConsoleAuditor]()
```

Any service with an `Auditor` field now receives `*ConsoleAuditor`, unless the host registered its own `Auditor` first.

## Hot-reloadable values

For configuration that must apply to live traffic without restarting, inject `*bosun.Dynamic[T]` and read it per request with `.Get()`.

```go
type RateLimit struct {
    Opts *bosun.Dynamic[RateLimitOptions]
}

func (m *RateLimit) Handle(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        per := m.Opts.Get().PerMinute // freshest value at request time
        ...
    })
}

var _ = bosun.DefaultDynamic[RateLimitOptions](func() *RateLimitOptions {
    return &RateLimitOptions{PerMinute: 60}
})
```

Swap the value at runtime with `Opts.Set(&newOpts)`, and react to changes with `Opts.OnChange(fn)`. The [config guide](./config.md) drives this from files and environment variables.

## Avoiding dependency cycles

The registry detects cycles at startup. If `A` injects `*B` and `B` injects `*A`, `app.Start()` returns an error naming the cycle. Break it by having one side depend on an interface that the other implements, or by communicating through an event instead of a direct reference.

## Validating the graph early

`app.Start()` walks the entire dependency graph. Missing dependencies, cycles, and failed `Init()` calls all surface there, before any traffic is served by `Run()`.

```go
if err := app.Start(); err != nil {
    log.Fatal(err)
}
```

The [testing guide](./testing.md) uses this same mechanism to swap stubs in for real services.
