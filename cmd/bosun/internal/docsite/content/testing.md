# Testing

You drive a Bosun app from a Go test by constructing the `App`, registering stubs, calling `Start()`, and serving requests through `httptest`. There is no process boundary and no network. This works because `registry.RegisterInstance[T]` beats any `bosun.Service[T]` or `bosun.Default[T]` declaration, so a stub registered first always wins, and `App` is itself an `http.Handler` that `httptest` drives directly. `app.ServeHTTP` includes app-wide middleware (`bosun.WithMiddleware`); `app.Mux.ServeHTTP` skips it and only exercises the routes.

## A handler test end to end

```go
func TestUsersGet(t *testing.T) {
    app := bosun.New()
    registry.RegisterInstance[users.Repo](app.Reg, &stubRepo{}) // override the real repo
    if err := app.Start(); err != nil {
        t.Fatal(err)
    }

    rec := httptest.NewRecorder()
    app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/users/1", nil))

    if rec.Code != 200 {
        t.Fatalf("status %d: %s", rec.Code, rec.Body)
    }
    if !strings.Contains(rec.Body.String(), `"id":1`) {
        t.Fatalf("body = %s", rec.Body)
    }
}
```

The stub is trivial to write when a controller depends on an interface rather than a concrete type. Have the production code bind the real implementation with `bosun.DefaultBind`, and let the test register the stub.

```go
type Repo interface {
    Find(id int) (*User, error)
}

var _ = bosun.Service[GormRepo]()
var _ = bosun.DefaultBind[Repo, GormRepo]()
```

```go
registry.RegisterInstance[Repo](app.Reg, &stubRepo{})
```

## Asserting on audit events

To test behavior that is not visible in the response (the cause of an error, or its origin), register a capturing auditor and assert on the events it collects. The audit event carries the full cause and the `bosun.E` origin.

```go
type capAuditor struct{ events []bosun.AuditEvent }

func (a *capAuditor) Audit(ctx context.Context, ev bosun.AuditEvent) {
    a.events = append(a.events, ev)
}

func TestUnauthorizedAudit(t *testing.T) {
    aud := &capAuditor{}
    app := bosun.New()
    registry.RegisterInstance[bosun.Auditor](app.Reg, aud)
    app.Start()

    // ...drive a request that fails auth...

    last := aud.events[len(aud.events)-1]
    if last.Status != 401 {
        t.Fatalf("audit status = %d", last.Status)
    }
}
```

## Testing middleware in isolation

Middleware is a registered type, so resolve it from the registry, wrap a verification handler, and drive it directly. This is how you assert both that a middleware blocks a request and that it attaches the expected typed context value.

```go
func TestRequireAuthAttachesUser(t *testing.T) {
    app := bosun.New()
    registry.RegisterInstance[*Sessions](app.Reg, &fakeSessions{user: &AuthUser{ID: 1}})
    app.Start()

    mw, _ := registry.Resolve[*RequireAuth](app.Reg)

    var seen *AuthUser
    h := mw.Handle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        seen = bosun.Value[AuthUser](r.Context())
    }))

    req := httptest.NewRequest("GET", "/", nil)
    req.Header.Set("Authorization", "Bearer ok")
    h.ServeHTTP(httptest.NewRecorder(), req)

    if seen == nil || seen.ID != 1 {
        t.Fatalf("user not attached: %+v", seen)
    }
}
```

## Database tests

Two patterns cover most cases. For a repository package, prefer a real database (a containerized Postgres or an in-memory SQLite) migrated in setup, so the SQL is exercised for real. For a controller or service test where the SQL does not matter, stub the repo interface, which is fast and isolated. Pick per package.

## Cleanup and shutdown

If a service starts a background goroutine in `Init()` (the config watcher, an event consumer, an outbox relay), call `app.Shutdown()` in `t.Cleanup` so it does not leak into later tests. `Shutdown` closes every registered `io.Closer` in reverse dependency order.

```go
t.Cleanup(func() { _ = app.Shutdown() })
```

## A note on shared state

Each `bosun.New()` owns its runtime state: its registry and service instances, its mux, its mounted typed routes (`app.TypedRoutes()`) and the statuses observed on them (`app.ObservedStatuses`). Route assertions therefore don't depend on test order, and tests that each build their own app can use `t.Parallel()`.

```go
func TestRoutes(t *testing.T) {
    t.Parallel()
    app := bosun.New(bosun.OverridePrefix[users.Controller]("/v2"))
    if err := app.Start(); err != nil {
        t.Fatal(err)
    }
    for _, rt := range app.TypedRoutes() { // only this app's routes
        ...
    }
}
```

Some state is still package-level. It consists of immutable declarations that are made in `init` and copied into each app at `New()` or `Start()`, so sharing it between tests is safe:

- `bosun.Service`, `Middleware`, `Controller`, `Default` and `DefaultBind` declarations. Each app builds its own instances from them, and `bosun.Disable` and `bosun.OverridePrefix` adjust them per app.
- Module-level declarations such as `eventmod.Declare`, `manifestmod.Register` contributors, webhook registrations and `openapi.Sources`.
- Audit redaction's sensitive field names.

The deprecated `bosun.TypedRoutes()` and `bosun.ObservedStatuses()` are the exception: they aggregate across every app in the process. Use the `app.` methods instead. Avoid sharing a single `App` between tests that need isolation.
