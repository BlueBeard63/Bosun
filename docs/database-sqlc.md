# sqlc integration

sqlc generates type-safe Go from raw SQL, and it fits Bosun the same way GORM does: register the connection and the generated `*Queries` as instances on the registry, then inject them into services. This guide shows the wiring, transactions, and testing.

## Setup

After running `sqlc generate`, open a connection pool and register both the pool and the generated `*Queries` struct. Register the pool as well as the queries so transactional paths can begin a transaction on it.

```go
func main() {
    pool, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
    if err != nil {
        log.Fatal(err)
    }
    queries := dbq.New(pool)

    app := bosun.New()
    registry.RegisterInstance[*pgxpool.Pool](app.Reg, pool)
    registry.RegisterInstance[*dbq.Queries](app.Reg, queries)
    log.Fatal(app.Run(":8080"))
}
```

Inject `*dbq.Queries` into a repository just like any other service, and pass the handler's context through so sqlc gets cancellation and deadlines.

```go
type UserRepo struct {
    q *dbq.Queries // injected
}

func (r *UserRepo) Get(ctx context.Context, id int64) (*dbq.User, error) {
    u, err := r.q.GetUser(ctx, id)
    if err != nil {
        return nil, err
    }
    return &u, nil
}

var _ = bosun.Service[UserRepo]()
```

## Transactions

Follow sqlc's pattern: begin a transaction on the pool, bind the queries to it with `WithTx`, and commit at the end. The injected pool is what makes this possible from a service.

```go
func (b *Billing) Charge(ctx context.Context, userID, cents int64) error {
    tx, err := b.Pool.Begin(ctx)
    if err != nil {
        return err
    }
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

## Hiding sqlc behind an interface

Depend on an interface where business code cares about behavior rather than the SQL implementation, and bind the sqlc implementation with `bosun.DefaultBind`. Tests then swap a fake with `registry.RegisterInstance`.

```go
type Users interface {
    Get(ctx context.Context, id int64) (*User, error)
}

var _ = bosun.Service[SqlcUsers]()
var _ = bosun.DefaultBind[Users, SqlcUsers]()
```

## Mapping errors

Catalogue the pgx and sqlc errors you care about and translate them at the handler boundary so internal causes never reach the client. The [errors guide](./errors.md) covers this in general.

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

## Migrations and shutdown

sqlc does not apply migrations, so pair it with a tool such as goose or golang-migrate and run migrations as a pre-start step in `main`; a failed migration should crash the process rather than start the server. For shutdown, register a small closer that calls `pool.Close()`, and `app.Shutdown()` will invoke it in reverse dependency order.

```go
type PoolCloser struct{ p *pgxpool.Pool }

func (c *PoolCloser) Close() error { c.p.Close(); return nil }

registry.RegisterInstance[*PoolCloser](app.Reg, &PoolCloser{p: pool})
```

## Testing

Build `*dbq.Queries` against a test pool (or a mock), register both instances on a fresh `App`, and drive `app.Mux` with `httptest`, exactly as in the [testing guide](./testing.md).
