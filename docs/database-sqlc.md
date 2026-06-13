# sqlc integration

sqlc generates type-safe Go from raw SQL. The integration with Bosun is
the same pattern as GORM: register the connection and the generated
`*Queries` as instances on the registry, then inject into services.

---

## Setup

### 1. Generate the queries

`sqlc.yaml`:

```yaml
version: "2"
sql:
  - engine: "postgresql"
    schema: "db/migrations"
    queries: "db/queries"
    gen:
      go:
        package: "dbq"
        out: "db/dbq"
        sql_package: "pgx/v5"
```

```sql
-- db/queries/users.sql
-- name: GetUser :one
SELECT id, email, name FROM users WHERE id = $1;

-- name: CreateUser :one
INSERT INTO users (email, name) VALUES ($1, $2) RETURNING id, email, name;
```

Run `sqlc generate`. You now have `db/dbq/{db.go, querier.go, users.sql.go}`.

### 2. Open a pool + register both

```go
package main

import (
    "context"
    "log"
    "os"

    "github.com/amberstack/bosun"
    "github.com/amberstack/bosun/registry"
    "github.com/jackc/pgx/v5/pgxpool"

    "myapp/db/dbq"
)

func main() {
    pool, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
    if err != nil { log.Fatal(err) }

    queries := dbq.New(pool)

    app := bosun.New()
    registry.RegisterInstance[*pgxpool.Pool](app.Reg, pool)   // for transactions
    registry.RegisterInstance[*dbq.Queries](app.Reg, queries) // for plain reads/writes

    log.Fatal(app.Run(":8080"))
}
```

### 3. Inject into a repo

```go
type UserRepo struct {
    q *dbq.Queries
}

func (r *UserRepo) Get(ctx context.Context, id int64) (*dbq.User, error) {
    u, err := r.q.GetUser(ctx, id)
    if err != nil { return nil, err }
    return &u, nil
}

var _ = bosun.Service[UserRepo]()
```

`*dbq.Queries` is the sqlc-generated struct; Bosun injects the registered
instance via the field type, just like any other service.

---

## Handler glue

```go
type GetIn struct { ID int64 `path:"id"` }
type UserOut struct {
    ID    int64  `json:"id"`
    Email string `json:"email"`
    Name  string `json:"name"`
}

type UsersController struct {
    Repo *UserRepo
}

var _ = bosun.Controller[UsersController]("/users")

func (c *UsersController) Routes(r *bosun.Router) {
    bosun.Get(r, "/:id", c.Get)
}

func (c *UsersController) Get(ctx context.Context, req *bosun.Req[GetIn]) (UserOut, error) {
    u, err := c.Repo.Get(ctx, req.Body.ID)
    if err != nil {
        if errors.Is(err, pgx.ErrNoRows) {
            return UserOut{}, bosun.E(http.StatusNotFound, "user not found", err)
        }
        return UserOut{}, bosun.E(http.StatusInternalServerError, "lookup failed", err)
    }
    return UserOut{ID: u.ID, Email: u.Email, Name: u.Name}, nil
}
```

`req.Body.ID` is the bound path parameter; `ctx` flows into sqlc for
cancellation and deadlines.

---

## Transactions

sqlc's pattern: open a tx on the pool, then `*Queries{}.WithTx(tx)`:

```go
type Billing struct {
    Pool *pgxpool.Pool   // injected
    Q    *dbq.Queries    // for read-only paths
}

var _ = bosun.Service[Billing]()

func (b *Billing) Charge(ctx context.Context, userID, cents int64) error {
    tx, err := b.Pool.Begin(ctx)
    if err != nil { return err }
    defer tx.Rollback(ctx)

    q := b.Q.WithTx(tx)
    if err := q.DebitWallet(ctx, dbq.DebitWalletParams{UserID: userID, Cents: cents}); err != nil {
        return err
    }
    if _, err := q.InsertCharge(ctx, dbq.InsertChargeParams{UserID: userID, Cents: cents}); err != nil {
        return err
    }
    return tx.Commit(ctx)
}
```

---

## Hiding sqlc behind an interface

Services that care about behavior, not the SQL implementation, depend on
an interface. Tests swap a fake; sqlc is just one implementation:

```go
// repo.go
type Users interface {
    Get(ctx context.Context, id int64) (*User, error)
    Create(ctx context.Context, in CreateUserIn) (*User, error)
}

// repo_sqlc.go
type SqlcUsers struct{ q *dbq.Queries }
func (r *SqlcUsers) Get(...) ...
func (r *SqlcUsers) Create(...) ...

var _ = bosun.Service[SqlcUsers]()
var _ = bosun.DefaultBind[Users, SqlcUsers]()
```

Now any service with a `Users` field gets `*SqlcUsers`; tests can override
with `registry.RegisterInstance[Users](app.Reg, &stubUsers{})`.

---

## Mapping errors

Catalogue the sqlc/pgx errors you care about and translate to `bosun.E`:

```go
func mapErr(err error) error {
    switch {
    case errors.Is(err, pgx.ErrNoRows):
        return bosun.E(http.StatusNotFound, "not found", err)
    case isUniqueViolation(err):
        return bosun.E(http.StatusConflict, "already exists", err)
    default:
        return bosun.E(http.StatusInternalServerError, "db error", err)
    }
}
```

Use it at the handler boundary so internal causes never leak to clients.

---

## Graceful shutdown

```go
type PoolCloser struct{ p *pgxpool.Pool }
func (c *PoolCloser) Close() error { c.p.Close(); return nil }

registry.RegisterInstance[*PoolCloser](app.Reg, &PoolCloser{p: pool})
```

`app.Shutdown()` calls `Close()` on every registered `io.Closer` in reverse
dependency order.

---

## Migrations

sqlc doesn't apply migrations — pair it with `goose`, `golang-migrate`, or
`atlas`. Run migrations as a pre-start step in `main`:

```go
if err := goose.Up(stdDB, "db/migrations"); err != nil { log.Fatal(err) }
```

Then start the app. Failed migrations should crash the process, not run
the server.

---

## Testing

Spin up `pgx`/`pgxpool` against a test database (or use `pgxmock`), build
`*dbq.Queries` against it, and register both as instances on a fresh
`App`:

```go
func TestUsersGet(t *testing.T) {
    pool := testPool(t)
    q := dbq.New(pool)

    app := bosun.New()
    registry.RegisterInstance[*pgxpool.Pool](app.Reg, pool)
    registry.RegisterInstance[*dbq.Queries](app.Reg, q)
    if err := app.Start(); err != nil { t.Fatal(err) }

    rec := httptest.NewRecorder()
    req := httptest.NewRequest("GET", "/users/1", nil)
    app.Mux.ServeHTTP(rec, req)
    // assert rec.Code / rec.Body
}
```
