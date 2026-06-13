# Testing

You can drive a Bosun app from a Go test by constructing the `App`,
registering stubs, calling `Start()`, and then hitting `app.Mux` through
`httptest`. No process boundary, no network.

---

## A handler test, end to end

```go
package users_test

import (
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "github.com/amberstack/bosun"
    "github.com/amberstack/bosun/registry"

    "myapp/users"
)

type stubRepo struct{}
func (*stubRepo) Find(id int) (*users.User, error) {
    return &users.User{ID: id, Name: "Jack"}, nil
}

func TestUsersGet(t *testing.T) {
    app := bosun.New()

    // override the real repo with a stub
    registry.RegisterInstance[users.Repo](app.Reg, &stubRepo{})

    if err := app.Start(); err != nil { t.Fatal(err) }

    rec := httptest.NewRecorder()
    req := httptest.NewRequest(http.MethodGet, "/users/1", nil)
    app.Mux.ServeHTTP(rec, req)

    if rec.Code != 200 { t.Fatalf("status %d: %s", rec.Code, rec.Body) }
    if !strings.Contains(rec.Body.String(), `"id":1`) {
        t.Fatalf("body = %s", rec.Body)
    }
}
```

Key points:

- `registry.RegisterInstance[T]` wins over any `bosun.Service[T]` /
  `bosun.Default[T]` declaration. Stubs go in first.
- `app.Mux` is a plain `*http.ServeMux` — `httptest` drives it directly,
  no listener needed.

---

## Tightening the registry: depend on interfaces

If `UsersController` injects a `Repo` interface (not `*GormRepo`), the
stub above is trivial. Pattern:

```go
// repo.go
type Repo interface {
    Find(id int) (*User, error)
    Create(in CreateUserIn) (*User, error)
}

// repo_gorm.go
type GormRepo struct{ db *gorm.DB }
func (r *GormRepo) Find(id int) (*User, error) { ... }
func (r *GormRepo) Create(in CreateUserIn) (*User, error) { ... }

var _ = bosun.Service[GormRepo]()
var _ = bosun.DefaultBind[Repo, GormRepo]()
```

Production: `Repo` resolves to `*GormRepo`. Tests register a stub:

```go
registry.RegisterInstance[Repo](app.Reg, &stubRepo{})
```

---

## Capturing audit events in tests

If your code path triggers `bosun.E(...)`, register a capturing
auditor and assert against the events:

```go
type capAuditor struct {
    events []bosun.AuditEvent
}
func (a *capAuditor) Audit(ctx context.Context, ev bosun.AuditEvent) {
    a.events = append(a.events, ev)
}

func TestUnauthorizedAudit(t *testing.T) {
    aud := &capAuditor{}
    app := bosun.New()
    registry.RegisterInstance[bosun.Auditor](app.Reg, aud)
    app.Start()

    rec := httptest.NewRecorder()
    req := httptest.NewRequest("POST", "/login", strings.NewReader(`{"email":"x","password":"bad"}`))
    req.Header.Set("Content-Type", "application/json")
    app.Mux.ServeHTTP(rec, req)

    last := aud.events[len(aud.events)-1]
    if last.Status != 401 { t.Fatalf("audit status = %d", last.Status) }
    if !strings.Contains(last.ErrOrigin, "auth.go") {
        t.Fatalf("origin = %q", last.ErrOrigin)
    }
}
```

The audit event carries the full cause + the `bosun.E(...)` origin —
useful for asserting on internals that aren't visible in the response.

---

## Middleware tests

Middleware is just a registered type. Wrap a simple sink:

```go
func TestRequireAuthBlocksUnauthenticated(t *testing.T) {
    app := bosun.New()
    registry.RegisterInstance[*Sessions](app.Reg, &emptySessions{})
    app.Start()

    mw, err := registry.Resolve[*RequireAuth](app.Reg)
    if err != nil { t.Fatal(err) }

    h := mw.Handle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        t.Fatal("should not reach handler")
    }))

    rec := httptest.NewRecorder()
    req := httptest.NewRequest("GET", "/", nil)
    h.ServeHTTP(rec, req)

    if rec.Code != 403 { t.Fatalf("status = %d", rec.Code) }
}
```

Or, drive it through the full app and assert on the response.

---

## Testing typed context values

If your middleware attaches `bosun.WithValue(ctx, user)`, downstream
handlers retrieve it via `bosun.Value[User](ctx)`. To unit-test a
middleware in isolation, install a verification handler:

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

    if seen == nil || seen.ID != 1 { t.Fatalf("user not attached: %+v", seen) }
}
```

---

## Database tests

Two common patterns:

### Real DB (preferred for repo tests)

Use a containerized Postgres (`testcontainers-go`) or a local test DB.
Migrate on `t.Setup`, register the real `*gorm.DB` / `*pgxpool.Pool`, and
run handlers end-to-end. Slow but high-fidelity.

### In-memory stub

For controller-level tests where you don't care about the SQL, stub the
repo interface as shown above. Fast and isolated.

Pick per package: repo packages get real-DB tests, handlers and services
get stubs.

---

## Goroutine leaks and shutdown

If your service spins a background goroutine in `Init()` (e.g. the config
watcher), call `app.Shutdown()` in `t.Cleanup` so it doesn't leak into
subsequent tests:

```go
t.Cleanup(func() { _ = app.Shutdown() })
```

`Shutdown` calls `Close()` on every registered `io.Closer` in reverse
dependency order.

---

## Parallel tests

Bosun maintains some package-level state (the route index, pending service
registrations) that lives across tests. Within one process they're
append-only and safe to share; just be aware that `bosun.TypedRoutes()`
returns the cumulative list, not per-app.

If you need fully isolated apps, run each test in its own process (or
just don't share `App` instances — each `New()` is independent for
runtime state).
