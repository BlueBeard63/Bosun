# Repo module

The `repomod` + `gormrepomod` modules give you a generic, injectable
`Repo[T]` for each entity — CRUD, a chainable query builder, and
transactions that span multiple repos. The interface is database-agnostic;
the bundled driver wraps GORM.

If you'd rather hand-roll a per-domain repo struct (the older pattern in
[`database-gorm.md`](./database-gorm.md)), you still can — this module
just productizes that pattern.

---

## Setup

### 1. Open the DB and register it

```go
package main

import (
    "log"

    "github.com/amberstack/bosun"
    "github.com/amberstack/bosun/registry"
    "gorm.io/driver/postgres"
    "gorm.io/gorm"
)

func main() {
    db, err := gorm.Open(postgres.Open("host=localhost user=app dbname=app sslmode=disable"))
    if err != nil { log.Fatal(err) }
    db.AutoMigrate(&User{})

    app := bosun.New()
    registry.RegisterInstance[*gorm.DB](app.Reg, db)
    log.Fatal(app.Run(":8080"))
}
```

### 2. Declare an entity and register its Repo

```go
import "github.com/amberstack/bosun/modules/gormrepomod"

type User struct {
    ID    uint   `gorm:"primaryKey"`
    Email string `gorm:"uniqueIndex"`
    Name  string
}

var _ = gormrepomod.For[User]()
```

`For[User]()` registers `*GormRepo[User]` as a service and binds
`repomod.Repo[User]` to it via `DefaultBind`. The host wins: if you
register your own `repomod.Repo[User]` before `app.Start()` (e.g. a
stub in tests, or a custom repo with hand-rolled SQL), the default is
skipped.

### 3. Inject the Repo into controllers/services

```go
import "github.com/amberstack/bosun/modules/repomod"

type UsersController struct {
    Users repomod.Repo[User]  // injected
}
var _ = bosun.Controller[UsersController]("/users")

func (c *UsersController) Routes(r *bosun.Router) {
    bosun.Post(r, "/",     c.Create)
    bosun.Get (r, "/{id}", c.Get)
}
```

Depend on the **interface** (`repomod.Repo[User]`), not the concrete
`*gormrepomod.GormRepo[User]` — that's what lets tests swap in fakes
without touching GORM.

---

## The Repo interface

```go
type Repo[T any] interface {
    Get(ctx context.Context, id any) (*T, error)
    Create(ctx context.Context, entity *T) error
    Update(ctx context.Context, entity *T) error
    Delete(ctx context.Context, id any) error
    Query() Query[T]
    Tx(ctx context.Context, fn func(ctx context.Context) error) error
}
```

`Get` and `Query.One` return `repomod.ErrNotFound` (not the driver's
sentinel) when no row matches; check with `errors.Is`.

---

## Querying

```go
rows, err := c.Users.Query().
    Where("email LIKE ?", "%@example.com").
    Order("created_at desc").
    Limit(20).
    All(ctx)

count, _ := c.Users.Query().Where("active = ?", true).Count(ctx)

one, err := c.Users.Query().Where("email = ?", email).One(ctx)
```

The query builder is immutable — every chain call returns a new query, so
you can share base queries safely.

For anything beyond Where/Order/Limit/Offset (joins, raw SQL, GORM
preloads), inject `*gorm.DB` alongside the repo and use it directly:

```go
type UsersController struct {
    Users repomod.Repo[User]
    DB    *gorm.DB  // escape hatch
}
```

---

## Transactions

```go
err := c.Users.Tx(ctx, func(ctx context.Context) error {
    u := &User{Email: in.Email, Name: in.Name}
    if err := c.Users.Create(ctx, u); err != nil { return err }
    return c.Orders.Create(ctx, &Order{UserID: u.ID, Total: 100})
})
```

The callback's `ctx` carries the active transaction. Any `Repo[T]` method
called with that ctx — including repos for other entity types — joins the
same transaction. Return a non-nil error to roll back; return nil to
commit. Nested `Tx` calls reuse the outermost transaction.

---

## Multiple databases

When you need different repos backed by different databases, define a
distinct named pointer type per DB and use `Named[T, DB]`:

```go
type PrimaryDB struct{ *gorm.DB }
func (p *PrimaryDB) Unwrap() *gorm.DB { return p.DB }

type AnalyticsDB struct{ *gorm.DB }
func (a *AnalyticsDB) Unwrap() *gorm.DB { return a.DB }

var _ = gormrepomod.Named[User,  *PrimaryDB]()
var _ = gormrepomod.Named[Event, *AnalyticsDB]()
```

Register each named DB in `main`:

```go
registry.RegisterInstance[*PrimaryDB]  (app.Reg, &PrimaryDB{DB: pdb})
registry.RegisterInstance[*AnalyticsDB](app.Reg, &AnalyticsDB{DB: adb})
```

A regular Go type alias (`type X = *gorm.DB`) won't work — it shares the
underlying `reflect.Type` with `*gorm.DB`, so the registry can't
distinguish it. A named struct wrapper is required.

`For[T]` and `Named[T, DB]` both bind `repomod.Repo[T]`, so only declare
one per entity type.

---

## Testing

For unit tests of services that depend on `Repo[T]`, register a stub
before `app.Start()`:

```go
type stubUsers struct {
    repomod.Repo[User] // optional: embed for unused methods
}
func (s *stubUsers) Get(ctx context.Context, id any) (*User, error) {
    return &User{ID: 1, Email: "alice@example.com"}, nil
}

app := bosun.New()
registry.RegisterInstance[repomod.Repo[User]](app.Reg, &stubUsers{})
app.Start()
```

`DefaultBind` checks the registry first, so the stub wins.

For integration tests of the GORM driver itself, an in-memory SQLite
gets you a real DB without spinning up infrastructure:

```go
import "gorm.io/driver/sqlite"

db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
db.AutoMigrate(&User{})
```

The module's own test suite in
[`modules/gormrepomod/gormrepomod_test.go`](../modules/gormrepomod/gormrepomod_test.go)
is a working reference covering CRUD, queries, single- and cross-repo
transactions, host overrides, and named-DB isolation.

---

## End-to-end example

Runnable demo: [`examples/repo/main.go`](../examples/repo/main.go).

```
go run ./examples/repo
curl -X POST localhost:8090/users -d '{"email":"alice@example.com"}'
curl localhost:8090/users/1
curl 'localhost:8090/users?q=example'
```
