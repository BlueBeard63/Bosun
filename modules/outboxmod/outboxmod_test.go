package outboxmod_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amberstack/bosun"
	"github.com/amberstack/bosun/modules/eventmemmod"
	"github.com/amberstack/bosun/modules/eventmod"
	"github.com/amberstack/bosun/modules/outboxmod"
	"github.com/amberstack/bosun/modules/repomod"
	"github.com/amberstack/bosun/registry"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func init() {
	outboxmod.For()
	eventmemmod.Default()
}

func setup(t *testing.T) *bosun.App {
	t.Helper()
	// A per-test named in-memory DB keeps tests isolated (a plain :memory: DSN
	// would give each pooled connection its own database).
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&outboxmod.Record{}); err != nil {
		t.Fatal(err)
	}
	app := bosun.New()
	registry.RegisterInstance[*gorm.DB](app.Reg, db)
	registry.RegisterInstance[*outboxmod.Options](app.Reg, &outboxmod.Options{Poll: 20 * time.Millisecond, Batch: 50})
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Shutdown() })
	return app
}

func resolve[T any](t *testing.T, app *bosun.App) T {
	t.Helper()
	v, err := registry.Resolve[T](app.Reg)
	if err != nil {
		t.Fatalf("resolve %T: %v", *new(T), err)
	}
	return v
}

func TestCommitPublishes(t *testing.T) {
	app := setup(t)
	ob := resolve[outboxmod.Outbox](t, app)
	repo := resolve[repomod.Repo[outboxmod.Record]](t, app)
	sub := resolve[eventmod.Subscriber](t, app)

	got := make(chan []byte, 1)
	if _, err := sub.Subscribe(context.Background(), "orders.placed", func(_ context.Context, d eventmod.Delivery) error {
		got <- d.Message().Data
		return d.Ack(context.Background())
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	err := repo.Tx(context.Background(), func(ctx context.Context) error {
		return ob.Enqueue(ctx, "orders.placed", []byte(`{"id":1}`), nil)
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}

	select {
	case data := <-got:
		if string(data) != `{"id":1}` {
			t.Fatalf("payload = %s", data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("committed event was never relayed")
	}
}

func TestRollbackDoesNotPublish(t *testing.T) {
	app := setup(t)
	ob := resolve[outboxmod.Outbox](t, app)
	repo := resolve[repomod.Repo[outboxmod.Record]](t, app)
	sub := resolve[eventmod.Subscriber](t, app)

	var delivered atomic.Int64
	if _, err := sub.Subscribe(context.Background(), "orders.rolledback", func(_ context.Context, d eventmod.Delivery) error {
		delivered.Add(1)
		return d.Ack(context.Background())
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	boom := errors.New("boom")
	err := repo.Tx(context.Background(), func(ctx context.Context) error {
		if err := ob.Enqueue(ctx, "orders.rolledback", []byte(`{"id":2}`), nil); err != nil {
			return err
		}
		return boom // roll back
	})
	if !errors.Is(err, boom) {
		t.Fatalf("expected rollback error, got %v", err)
	}

	time.Sleep(200 * time.Millisecond) // give the relay several ticks
	if n := delivered.Load(); n != 0 {
		t.Fatalf("rolled-back event was published %d times", n)
	}
	// and no row should remain
	count, err := repo.Query().Count(context.Background())
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("outbox has %d rows after rollback, want 0", count)
	}
}
