# GORM integration

Bosun has no opinions about your data layer. You register `*gorm.DB` as an
instance on the registry; services that need it declare a `*gorm.DB` field
and the framework wires it.

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

    app := bosun.New()
    registry.RegisterInstance[*gorm.DB](app.Reg, db)

    log.Fatal(app.Run(":8080"))
}
```

That single `RegisterInstance` line makes `*gorm.DB` available everywhere.

### 2. Inject into services

```go
type UserRepo struct {
    db *gorm.DB    // injected
}

func (r *UserRepo) Find(id int) (*User, error) {
    var u User
    if err := r.db.First(&u, id).Error; err != nil { return nil, err }
    return &u, nil
}

var _ = bosun.Service[UserRepo]()
```

### 3. Inject into the controller

```go
type UsersController struct {
    Repo *UserRepo
}

var _ = bosun.Controller[UsersController]("/users")

func (c *UsersController) Routes(r *bosun.Router) {
    bosun.Get(r, "/:id", c.Get)
}

func (c *UsersController) Get(ctx context.Context, req *bosun.Req[GetIn]) (UserOut, error) {
    u, err := c.Repo.Find(req.Body.ID)
    if err != nil {
        if errors.Is(err, gorm.ErrRecordNotFound) {
            return UserOut{}, bosun.E(http.StatusNotFound, "user not found", err)
        }
        return UserOut{}, bosun.E(http.StatusInternalServerError, "lookup failed", err)
    }
    return UserOut{ID: u.ID, Name: u.Name}, nil
}
```

---

## Models

Standard GORM. Nothing Bosun-specific:

```go
type User struct {
    ID        uint      `gorm:"primaryKey"`
    Email     string    `gorm:"uniqueIndex;not null"`
    Name      string
    CreatedAt time.Time
}
```

Auto-migrate on startup:

```go
db.AutoMigrate(&User{}, &Org{})
```

---

## Putting it together: typed in/out + GORM

```go
type CreateUserIn struct {
    Email string `json:"email"`
    Name  string `json:"name"`
}

type UserOut struct {
    ID    uint   `json:"id"`
    Email string `json:"email"`
    Name  string `json:"name"`
}

type UserRepo struct {
    db *gorm.DB
}

func (r *UserRepo) Create(in CreateUserIn) (*User, error) {
    u := &User{Email: in.Email, Name: in.Name}
    return u, r.db.Create(u).Error
}

var _ = bosun.Service[UserRepo]()

type UsersController struct {
    Repo *UserRepo
}

var _ = bosun.Controller[UsersController]("/users")

func (c *UsersController) Routes(r *bosun.Router) {
    bosun.Post(r, "/", c.Create)
}

func (c *UsersController) Create(ctx context.Context, req *bosun.Req[CreateUserIn]) (UserOut, error) {
    u, err := c.Repo.Create(req.Body)
    if err != nil {
        if isUniqueViolation(err) {
            return UserOut{}, bosun.E(http.StatusConflict, "email already in use", err)
        }
        return UserOut{}, bosun.E(http.StatusInternalServerError, "create failed", err)
    }
    return UserOut{ID: u.ID, Email: u.Email, Name: u.Name}, nil
}
```

---

## Request-scoped DB sessions

GORM's `db.WithContext(ctx)` carries the request context into queries (for
deadlines, cancellation, tracing). Make it a habit at the repo boundary:

```go
func (r *UserRepo) Find(ctx context.Context, id int) (*User, error) {
    var u User
    return &u, r.db.WithContext(ctx).First(&u, id).Error
}
```

Pass `ctx` from your handler:

```go
func (c *UsersController) Get(ctx context.Context, req *bosun.Req[GetIn]) (UserOut, error) {
    u, err := c.Repo.Find(ctx, req.Body.ID)
    ...
}
```

---

## Transactions

GORM's `db.Transaction(func(tx *gorm.DB) error { ... })` works as usual:

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

Return errors and Bosun maps them: `bosun.E(http.StatusConflict, ...)` for
known business errors, plain errors for `500`s.

---

## Tip: hide GORM behind a repo interface

If you might swap GORM later (sqlc, sqlx, raw `database/sql`), depend on
an interface in business code and inject the GORM implementation:

```go
type Users interface {
    Find(ctx context.Context, id int) (*User, error)
    Create(ctx context.Context, in CreateUserIn) (*User, error)
}

// repo_gorm.go
type GormUsers struct{ db *gorm.DB }
func (r *GormUsers) Find(...) ...
func (r *GormUsers) Create(...) ...

var _ = bosun.Service[GormUsers]()
var _ = bosun.DefaultBind[Users, GormUsers]()
```

Services depend on the `Users` interface; tests swap in a stub via
`registry.RegisterInstance[Users](app.Reg, stub)`.

---

## Graceful shutdown

GORM doesn't expose `Close()` on `*gorm.DB`; grab the underlying
`*sql.DB`:

```go
type GormCloser struct{ db *gorm.DB }
func (c *GormCloser) Close() error {
    sqlDB, err := c.db.DB()
    if err != nil { return err }
    return sqlDB.Close()
}

registry.RegisterInstance[*GormCloser](app.Reg, &GormCloser{db: db})
```

`app.Shutdown()` calls `Close()` on every registered service implementing
`io.Closer`, in reverse dependency order.

---

## Audit redaction with GORM models

GORM models often have fields like `PasswordHash` or `APIKey`. Bosun's
audit redactor walks `req.Body` and the response value — if you return a
model directly, those fields get redacted by name (`password`/`apikey`
matches). For other sensitive fields tag explicitly:

```go
type User struct {
    ID       uint
    Email    string
    Secret   string `audit:"-"`
}
```
