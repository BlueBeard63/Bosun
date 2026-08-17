# Repo module

The `repomod` and `gormrepomod` modules provide a generic, injectable `Repo[T]` for each entity, with CRUD, a chainable query builder, and transactions that span several repos. The interface is database-agnostic, and the bundled driver wraps GORM. If you prefer a hand-rolled repository per domain, the pattern in the [GORM guide](./database-gorm.md) still works; this module simply packages it.

## Setup

Open the database and register the handle, then declare each entity's repo, then inject the interface where you need it.

```go
func main() {
    db, err := gorm.Open(postgres.Open("host=localhost user=app dbname=app sslmode=disable"))
    if err != nil {
        log.Fatal(err)
    }
    db.AutoMigrate(&User{})

    app := bosun.New()
    registry.RegisterInstance[*gorm.DB](app.Reg, db)
    log.Fatal(app.Run(":8080"))
}
```

```go
import "github.com/amberstack/bosun/modules/gormrepomod"

type User struct {
    ID    uint   `gorm:"primaryKey"`
    Email string `gorm:"uniqueIndex"`
    Name  string
}

var _ = gormrepomod.For[User]()
```

`For[User]()` registers `*GormRepo[User]` as a service and binds `repomod.Repo[User]` to it. The host wins, so if you register your own `repomod.Repo[User]` before `app.Start()` (a stub in a test, or a custom repo with hand-written SQL), the default is skipped.

```go
import "github.com/amberstack/bosun/modules/repomod"

type UsersController struct {
    Users repomod.Repo[User] // injected
}
```

Depend on the interface `repomod.Repo[User]`, not the concrete `*gormrepomod.GormRepo[User]`, because that is what lets tests swap in a fake without touching GORM.

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

`Get` and `Query().One` return the portable `repomod.ErrNotFound` (not the driver's own sentinel) when no row matches, so check with `errors.Is`.

## Querying

The query builder is immutable: each call returns a new query, so a base query is safe to share. For anything beyond `Where`, `Order`, `Limit`, and `Offset` (joins, raw SQL, GORM preloads), inject `*gorm.DB` alongside the repo and use it directly.

```go
rows, err := c.Users.Query().
    Where("email LIKE ?", "%@example.com").
    Order("created_at desc").
    Limit(20).
    All(ctx)
```

## Transactions

`Tx` runs a function inside a transaction. The callback's context carries the active transaction, and any `Repo[T]` method called with that context (including repos for other entity types) joins the same transaction. Returning a non-nil error rolls back, and nested `Tx` calls reuse the outermost transaction.

```go
err := c.Users.Tx(ctx, func(ctx context.Context) error {
    if err := c.Users.Create(ctx, u); err != nil {
        return err
    }
    return c.Orders.Create(ctx, &Order{UserID: u.ID})
})
```

## Multiple databases

To back different repos with different databases, define a distinct named pointer type per database that exposes `Unwrap`, register each one, and bind repos with `Named[T, DB]`.

```go
type PrimaryDB struct{ *gorm.DB }
func (p *PrimaryDB) Unwrap() *gorm.DB { return p.DB }

var _ = gormrepomod.Named[User, *PrimaryDB]()
```

A plain Go type alias does not work, because it shares the underlying `reflect.Type` with `*gorm.DB` and the registry cannot tell them apart; a named struct wrapper is required. Declare only one of `For[T]` or `Named[T, DB]` per entity, since both bind `repomod.Repo[T]`.

## Testing

For a service that depends on `Repo[T]`, register a stub before `app.Start()`; because the binding yields to the host, the stub wins.

```go
registry.RegisterInstance[repomod.Repo[User]](app.Reg, &stubUsers{})
```

For integration tests of the driver itself, an in-memory SQLite gives you a real database without infrastructure. The module's own suite in `modules/gormrepomod/gormrepomod_test.go` is a working reference covering CRUD, queries, single- and cross-repo transactions, host overrides, and named-database isolation.

```go
db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
db.AutoMigrate(&User{})
```

A runnable end-to-end demo lives in `examples/repo/main.go`.
