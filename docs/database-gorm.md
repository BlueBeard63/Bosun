# GORM integration

Bosun has no opinion about your data layer. You register `*gorm.DB` as an instance on the registry, and any service that declares a `*gorm.DB` field receives it. This guide covers the bare-metal pattern of injecting GORM directly and writing per-domain repository structs by hand. If you would rather have a generic `Repo[T]` with CRUD, a chainable query builder, and cross-repo transactions, use the [repo module](./repo.md) instead.

## Setup

Open the database and register the handle in `main`; that single line makes `*gorm.DB` available everywhere. Then inject it into a repository service, and inject that service into a controller.

```go
func main() {
    db, err := gorm.Open(postgres.Open("host=localhost user=app dbname=app sslmode=disable"))
    if err != nil {
        log.Fatal(err)
    }
    app := bosun.New()
    registry.RegisterInstance[*gorm.DB](app.Reg, db)
    log.Fatal(app.Run(":8080"))
}
```

```go
type UserRepo struct {
    db *gorm.DB // injected
}

func (r *UserRepo) Find(ctx context.Context, id int) (*User, error) {
    var u User
    return &u, r.db.WithContext(ctx).First(&u, id).Error
}

var _ = bosun.Service[UserRepo]()
```

## Request-scoped sessions

Carry the request context into every query with `db.WithContext(ctx)`, so deadlines, cancellation, and tracing propagate. Make it a habit at the repository boundary and pass the handler's `ctx` down to it.

## Transactions

GORM's `db.Transaction` works as usual. Return an error to roll back, and let Bosun map the error at the handler boundary: `bosun.E(http.StatusConflict, ...)` for a known business error, or a plain error for a 500.

```go
func (s *Billing) Charge(ctx context.Context, userID uint, cents int) error {
    return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
        if err := tx.Model(&Wallet{}).Where("user_id = ?", userID).
            Update("balance_cents", gorm.Expr("balance_cents - ?", cents)).Error; err != nil {
            return err
        }
        return tx.Create(&Charge{UserID: userID, Cents: cents}).Error
    })
}
```

## Hiding GORM behind an interface

If you might swap the data layer later, depend on an interface in business code and bind the GORM implementation with `bosun.DefaultBind`. Services then depend on the interface, and tests swap in a stub with `registry.RegisterInstance`.

```go
type Users interface {
    Find(ctx context.Context, id int) (*User, error)
}

var _ = bosun.Service[GormUsers]()
var _ = bosun.DefaultBind[Users, GormUsers]()
```

## Graceful shutdown

`*gorm.DB` has no `Close`, so wrap the underlying `*sql.DB` in a small closer and register it. `app.Shutdown()` calls `Close` on every registered `io.Closer` in reverse dependency order.

```go
type GormCloser struct{ db *gorm.DB }

func (c *GormCloser) Close() error {
    sqlDB, err := c.db.DB()
    if err != nil {
        return err
    }
    return sqlDB.Close()
}

registry.RegisterInstance[*GormCloser](app.Reg, &GormCloser{db: db})
```

## Audit redaction with models

The audit redactor walks `req.Body` and the response value, so a model field named `password` or `apikey` is redacted automatically when you return the model. Tag any other sensitive field with `audit:"-"`.

```go
type User struct {
    ID     uint
    Email  string
    Secret string `audit:"-"`
}
```
