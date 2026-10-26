# Testing

You drive a Bosun app from a Go test by constructing the `App`, swapping in fakes, calling `Start()`, and serving requests through `httptest`. There is no process boundary and no network, because `App` is itself an `http.Handler`. The `bosuntest` package wraps that in a few helpers: `bosuntest.App` builds and starts an app, `bosuntest.Get`/`Post`/... send requests to it, and `bosuntest.Build` constructs a single service with only its dependencies. Fakes are registered with `bosuntest.With[T]`, which wins over `bosun.Default` and `bosun.DefaultBind` and replaces a registered `bosun.Service`.

## A route test in three lines

```go
import "github.com/bluebeard63/bosun/bosuntest"

func TestUsersGet(t *testing.T) {
    app := bosuntest.App(t, bosuntest.With[users.Repo](&stubRepo{}))
    res := bosuntest.Get(t, app, "/users/1")
    u := bosuntest.Decode[users.UserOut](t, res)

    if res.Status != 200 || u.ID != 1 {
        t.Fatalf("status %d: %s", res.Status, res)
    }
}
```

`bosuntest.App(t, opts...)` runs `bosun.New`, registers the overrides, and calls `Start`, so the real routing, binding, validation and middleware run. The test fails if `Start` returns an error, and `app.Shutdown` runs in `t.Cleanup`. Pass `bosun.New` options through with `bosuntest.AppOptions(bosun.Disable(...), bosun.WithMiddleware(...))`.

Requests go through `app.ServeHTTP`, so app-wide middleware (`bosun.WithMiddleware`) is included:

- `bosuntest.Get`, `Post`, `Put`, `Patch` and `Delete(t, app, path, opts...)` build the request. `bosuntest.Do(t, app, req)` sends an `*http.Request` you built yourself.
- Request options: `bosuntest.JSON(v)` (JSON body and `Content-Type`), `bosuntest.Body(b)`, `bosuntest.Header(k, v)`, `bosuntest.Query(k, v)` (merged with any query already in the path) and `bosuntest.Session(id)` (see below).
- The result is a `*bosuntest.Response` with `Status`, `Header` and `Body []byte`. `bosuntest.Decode[Out](t, res)` decodes the body into the route's `Out` type and fails the test on invalid JSON. (It is not called `JSON` because that name is the request-body option.)

The fake is trivial to write when a controller depends on an interface rather than a concrete type. Have the production code bind the real implementation with `bosun.DefaultBind`, and let the test swap in the fake:

```go
type Repo interface {
    Find(id int) (*User, error)
}

var _ = bosun.Service[GormRepo]()
var _ = bosun.DefaultBind[Repo, GormRepo]()
```

`Start` builds every registered service, not only the ones the route uses, so the test must provide every external resource `main` registers (a `*gorm.DB`, an HTTP client, ...), either as a real test instance or a fake: `bosuntest.With(testDB)`.

## Building one service

To unit-test a service from another package, build it with fake leaves. Its injected fields are usually unexported, so you can't construct it by hand:

```go
svc := bosuntest.Build[*users.UsersService](t,
    bosuntest.With(testDB),                     // *gorm.DB
    bosuntest.With[users.Store](fakeStore{}),   // interface bound with DefaultBind
)
```

`Build[T]` registers the overrides, applies module defaults (`bosun.Default`, `bosun.DefaultBind`), and resolves `T`, which constructs `T` and only what `T` depends on. The app is not started: unrelated services are not built and no controller is mounted. The test fails if `T` or anything it needs can't be constructed, and the services that were built are shut down in `t.Cleanup`.

## Test-only sessions and auth

Test-only behavior comes from swapping a middleware's **dependency** with a fake defined in a `_test.go` file, not from a switch inside the middleware. `_test.go` files are never compiled into a production binary, so the compiler guarantees the fake session table can't ship.

```go
// auth.go (production)
type Sessions interface {
    Lookup(id string) (*AuthUser, error)
}

type RequireAuth struct {
    Sessions Sessions // injected
}

func (m *RequireAuth) Handle(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        id, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
        u, err := m.Sessions.Lookup(id)
        if !ok || err != nil {
            http.Error(w, "unauthorized", http.StatusUnauthorized)
            return
        }
        next.ServeHTTP(w, r.WithContext(bosun.WithValue(r.Context(), u)))
    })
}

var _ = bosun.Middleware[RequireAuth]()
var _ = bosun.Service[RedisSessions]()
var _ = bosun.DefaultBind[Sessions, RedisSessions]()
```

```go
// auth_test.go
type fakeSessions map[string]*AuthUser // or load it from testdata/sessions.json

func (s fakeSessions) Lookup(id string) (*AuthUser, error) {
    if u, ok := s[id]; ok {
        return u, nil
    }
    return nil, errors.New("no session")
}

func TestMe(t *testing.T) {
    app := bosuntest.App(t, bosuntest.With[Sessions](fakeSessions{"test-admin": admin}))
    res := bosuntest.Get(t, app, "/me", bosuntest.Session("test-admin"))
    if res.Status != 200 {
        t.Fatalf("status %d: %s", res.Status, res)
    }
}
```

The real `RequireAuth` still runs: it parses the header, takes its 401 path for unknown ids, and attaches the user to the context. Only the session lookup is fake.

The framework doesn't own session transport, so `bosuntest.Session(id)` sends the id the way your middleware reads it. The default is `Authorization: Bearer <id>`. Configure the app for anything else:

```go
app := bosuntest.App(t, with, bosuntest.SessionCookie("sid"))           // Cookie: sid=<id>
app := bosuntest.App(t, with, bosuntest.SessionHeader("X-Session", "")) // X-Session: <id>
```

Do **not** add a test bypass to production code:

```go
// Don't: this ships in the production binary, and one misconfigured
// environment variable turns it into an authentication bypass.
if os.Getenv("APP_ENV") == "test" && id == "test-admin" {
    return testAdmin, nil
}
```

Anything gated on an environment variable, a build flag set at deploy time, or a magic id is compiled into the binary you ship. A fake in `_test.go` is not.

## Overriding a registered service

`bosun.New()` registers every `bosun.Service`, `Middleware` and `Controller` type straight away, so `registry.RegisterInstance` for one of those types panics as a duplicate. `bosuntest.With[T]` uses `registry.Replace[T]` instead, which swaps the provider for a ready instance; every dependent built afterwards receives it:

```go
users := bosuntest.Build[*users.UsersService](t, bosuntest.With[users.Store](fakeStore{}))
shows := bosuntest.Build[*shows.ShowsService](t, bosuntest.With(users)) // gets this users
```

Outside `bosuntest`, call `registry.Replace[T](app.Reg, v)` after `bosun.New` and before `Start`. Replace only rewires a graph that hasn't used `T` yet: it panics if `T` has already been constructed, if a service that depends on `T` has been built, or after `Shutdown`. Replacing a middleware type works the same way, but prefer swapping its dependency as above, so the real middleware code stays under test.

Without `bosuntest`, the same steps are: `bosun.New()`, `registry.RegisterInstance` (or `registry.Replace`) the fakes on `app.Reg`, then `app.Start()` and `app.ServeHTTP(rec, req)` with `httptest`. `app.ServeHTTP` includes app-wide middleware; `app.Mux.ServeHTTP` skips it and only exercises the routes.

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

Middleware is a registered type, so build it with `bosuntest.Build`, wrap a verification handler, and drive it directly. This is how you assert both that a middleware blocks a request and that it attaches the expected typed context value.

```go
func TestRequireAuthAttachesUser(t *testing.T) {
    mw := bosuntest.Build[*RequireAuth](t,
        bosuntest.With[Sessions](fakeSessions{"ok": {ID: 1}}))

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

If a service starts a background goroutine in `Init()` (the config watcher, an event consumer, an outbox relay), call `app.Shutdown()` in `t.Cleanup` so it does not leak into later tests. `bosuntest.App` and `bosuntest.Build` do this for you. `Shutdown` closes every registered `io.Closer` in reverse dependency order.

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
