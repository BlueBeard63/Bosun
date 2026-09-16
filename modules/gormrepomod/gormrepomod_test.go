package gormrepomod_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/modules/gormrepomod"
	"github.com/bluebeard63/bosun/modules/repomod"
	"github.com/bluebeard63/bosun/registry"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// --- fixtures: entity types and named DB types registered at package scope ---

type tUser struct {
	ID    uint `gorm:"primaryKey"`
	Email string
}

type tOrder struct {
	ID     uint `gorm:"primaryKey"`
	UserID uint
	Total  int
}

type tEvent struct {
	ID   uint `gorm:"primaryKey"`
	Name string
}

type tMetric struct {
	ID    uint `gorm:"primaryKey"`
	Label string
}

// primaryDB and analyticsDB are named pointer types so the registry can
// distinguish them — type aliases would share reflect.Type.
type primaryDB struct{ *gorm.DB }

func (p *primaryDB) Unwrap() *gorm.DB { return p.DB }

type analyticsDB struct{ *gorm.DB }

func (a *analyticsDB) Unwrap() *gorm.DB { return a.DB }

var _ = gormrepomod.For[tUser]()
var _ = gormrepomod.For[tOrder]()
var _ = gormrepomod.Named[tEvent, *analyticsDB]()
var _ = gormrepomod.Named[tMetric, *primaryDB]()

// --- helpers ---

func newDB(t *testing.T, models ...any) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(models...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// newApp seeds a fresh app with the default DB plus optional named DBs and
// runs Start. Use this for tests that only need single-DB.
func newApp(t *testing.T, db *gorm.DB) *bosun.App {
	t.Helper()
	app := bosun.New()
	registry.RegisterInstance[*gorm.DB](app.Reg, db)
	// supply zero-value named DBs so Validate doesn't fail on the Named
	// repos registered at package scope
	registry.RegisterInstance[*primaryDB](app.Reg, &primaryDB{DB: db})
	registry.RegisterInstance[*analyticsDB](app.Reg, &analyticsDB{DB: db})
	if err := app.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	return app
}

func repoOf[T any](t *testing.T, app *bosun.App) repomod.Repo[T] {
	t.Helper()
	v, err := app.Reg.ResolveType(reflect.TypeOf((*repomod.Repo[T])(nil)).Elem())
	if err != nil {
		t.Fatalf("resolve Repo[%T]: %v", *new(T), err)
	}
	return v.(repomod.Repo[T])
}

// --- CRUD ---

func TestCRUDRoundTrip(t *testing.T) {
	db := newDB(t, &tUser{}, &tOrder{})
	app := newApp(t, db)
	users := repoOf[tUser](t, app)
	ctx := context.Background()

	u := &tUser{Email: "alice@example.com"}
	if err := users.Create(ctx, u); err != nil {
		t.Fatalf("create: %v", err)
	}
	if u.ID == 0 {
		t.Fatal("Create should populate primary key")
	}

	got, err := users.Get(ctx, u.ID)
	if err != nil || got.Email != "alice@example.com" {
		t.Fatalf("get: %v / %+v", err, got)
	}

	got.Email = "alice2@example.com"
	if err := users.Update(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	again, _ := users.Get(ctx, u.ID)
	if again.Email != "alice2@example.com" {
		t.Fatalf("update not persisted: %+v", again)
	}

	if err := users.Delete(ctx, u.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := users.Get(ctx, u.ID); !errors.Is(err, repomod.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestGetMissingReturnsErrNotFound(t *testing.T) {
	app := newApp(t, newDB(t, &tUser{}))
	users := repoOf[tUser](t, app)

	if _, err := users.Get(context.Background(), 999); !errors.Is(err, repomod.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// --- query builder ---

func TestQueryChainable(t *testing.T) {
	app := newApp(t, newDB(t, &tUser{}))
	users := repoOf[tUser](t, app)
	ctx := context.Background()

	for _, e := range []string{"a@x.com", "b@x.com", "c@y.com", "d@y.com"} {
		if err := users.Create(ctx, &tUser{Email: e}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	got, err := users.Query().
		Where("email LIKE ?", "%@y.com").
		Order("email asc").
		Limit(1).
		All(ctx)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 || got[0].Email != "c@y.com" {
		t.Fatalf("unexpected query result: %+v", got)
	}

	n, err := users.Query().Where("email LIKE ?", "%@x.com").Count(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("count want 2, got %d", n)
	}
}

// --- transactions ---

func TestTxCommitAndRollback(t *testing.T) {
	app := newApp(t, newDB(t, &tUser{}))
	users := repoOf[tUser](t, app)
	ctx := context.Background()

	// commit
	err := users.Tx(ctx, func(ctx context.Context) error {
		return users.Create(ctx, &tUser{Email: "committed@example.com"})
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
	n, _ := users.Query().Where("email = ?", "committed@example.com").Count(ctx)
	if n != 1 {
		t.Fatal("commit did not persist row")
	}

	// rollback
	sentinel := errors.New("rollback")
	err = users.Tx(ctx, func(ctx context.Context) error {
		if err := users.Create(ctx, &tUser{Email: "rolledback@example.com"}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel, got %v", err)
	}
	n, _ = users.Query().Where("email = ?", "rolledback@example.com").Count(ctx)
	if n != 0 {
		t.Fatal("rolled-back row should not be visible")
	}
}

// Cross-repo Tx — verifies the ctx-stash mechanism: a single Tx block
// covers operations on two different Repo[T] instances.
func TestCrossRepoTxRollback(t *testing.T) {
	app := newApp(t, newDB(t, &tUser{}, &tOrder{}))
	users := repoOf[tUser](t, app)
	orders := repoOf[tOrder](t, app)
	ctx := context.Background()

	sentinel := errors.New("rollback")
	err := users.Tx(ctx, func(ctx context.Context) error {
		u := &tUser{Email: "tx@example.com"}
		if err := users.Create(ctx, u); err != nil {
			return err
		}
		if err := orders.Create(ctx, &tOrder{UserID: u.ID, Total: 100}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel, got %v", err)
	}

	uCount, _ := users.Query().Where("email = ?", "tx@example.com").Count(ctx)
	oCount, _ := orders.Query().Where("total = ?", 100).Count(ctx)
	if uCount != 0 || oCount != 0 {
		t.Fatalf("cross-repo rollback failed: users=%d orders=%d", uCount, oCount)
	}
}

// --- override / mocking ---

type stubUsers struct {
	repomod.Repo[tUser]
	calls int
}

func (s *stubUsers) Get(ctx context.Context, id any) (*tUser, error) {
	s.calls++
	return &tUser{ID: 42, Email: "stub@example.com"}, nil
}

func TestHostOverrideBeatsDefaultBind(t *testing.T) {
	app := bosun.New()
	registry.RegisterInstance[*gorm.DB](app.Reg, newDB(t, &tUser{}, &tOrder{}))
	registry.RegisterInstance[*primaryDB](app.Reg, &primaryDB{})
	registry.RegisterInstance[*analyticsDB](app.Reg, &analyticsDB{})

	stub := &stubUsers{}
	registry.RegisterInstance[repomod.Repo[tUser]](app.Reg, stub)

	if err := app.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	users := repoOf[tUser](t, app)
	if _, err := users.Get(context.Background(), 1); err != nil {
		t.Fatalf("stub get: %v", err)
	}
	if stub.calls != 1 {
		t.Fatalf("stub not called: %d", stub.calls)
	}
}

// --- multi-DB ---

func TestNamedDB_Isolation(t *testing.T) {
	pdb := newDB(t, &tMetric{})
	// Use a separate connection string so analytics has its own DB.
	adb, err := gorm.Open(sqlite.Open("file:analytics?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open analytics: %v", err)
	}
	if err := adb.AutoMigrate(&tEvent{}); err != nil {
		t.Fatalf("migrate analytics: %v", err)
	}

	app := bosun.New()
	registry.RegisterInstance[*gorm.DB](app.Reg, pdb) // for tUser/tOrder package-scope For
	registry.RegisterInstance[*primaryDB](app.Reg, &primaryDB{DB: pdb})
	registry.RegisterInstance[*analyticsDB](app.Reg, &analyticsDB{DB: adb})
	if err := app.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	metrics := repoOf[tMetric](t, app)
	events := repoOf[tEvent](t, app)
	ctx := context.Background()

	if err := metrics.Create(ctx, &tMetric{Label: "cpu"}); err != nil {
		t.Fatalf("create metric: %v", err)
	}
	if err := events.Create(ctx, &tEvent{Name: "boot"}); err != nil {
		t.Fatalf("create event: %v", err)
	}

	// Each table must exist in only one DB.
	if err := pdb.Migrator().HasTable(&tEvent{}); err {
		t.Fatal("tEvent should not be in primary DB")
	}
	if err := adb.Migrator().HasTable(&tMetric{}); err {
		t.Fatal("tMetric should not be in analytics DB")
	}

	n, _ := metrics.Query().Count(ctx)
	if n != 1 {
		t.Fatalf("metric count want 1, got %d", n)
	}
	n, _ = events.Query().Count(ctx)
	if n != 1 {
		t.Fatalf("event count want 1, got %d", n)
	}
}
