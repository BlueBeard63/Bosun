package tenantmod_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bluebeard63/bosun/modules/gormrepomod"
	"github.com/bluebeard63/bosun/modules/repomod"
	"github.com/bluebeard63/bosun/modules/tenantmod"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type User struct {
	ID       uint `gorm:"primaryKey"`
	TenantID string
	Name     string
}

func newRepo(t *testing.T) repomod.Repo[User] {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&User{}); err != nil {
		t.Fatal(err)
	}
	return tenantmod.Scope[User](&gormrepomod.GormRepo[User]{DB: db}, "tenant_id")
}

func TestScopedIsolation(t *testing.T) {
	repo := newRepo(t)
	a := tenantmod.WithTenant(context.Background(), "A")
	b := tenantmod.WithTenant(context.Background(), "B")

	if err := repo.Create(a, &User{ID: 1, Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Create(b, &User{ID: 2, Name: "bob"}); err != nil {
		t.Fatal(err)
	}

	// Create stamped the tenant column.
	u, err := repo.Get(a, 1)
	if err != nil {
		t.Fatalf("get own: %v", err)
	}
	if u.TenantID != "A" {
		t.Fatalf("tenant stamped %q, want A", u.TenantID)
	}

	// A sees only its own rows.
	all, _ := repo.Query().All(a)
	if len(all) != 1 || all[0].Name != "alice" {
		t.Fatalf("tenant A list = %v", all)
	}
	if n, _ := repo.Query().Count(a); n != 1 {
		t.Fatalf("tenant A count = %d, want 1", n)
	}

	// A cannot read or delete B's row.
	if _, err := repo.Get(a, 2); !errors.Is(err, repomod.ErrNotFound) {
		t.Fatalf("cross-tenant get = %v, want ErrNotFound", err)
	}
	if err := repo.Delete(a, 2); !errors.Is(err, repomod.ErrNotFound) {
		t.Fatalf("cross-tenant delete = %v, want ErrNotFound", err)
	}

	// B's row is intact.
	if _, err := repo.Get(b, 2); err != nil {
		t.Fatalf("B lost its own row: %v", err)
	}
}

func TestNoTenantIsAnError(t *testing.T) {
	repo := newRepo(t)
	if err := repo.Create(context.Background(), &User{ID: 1}); !errors.Is(err, tenantmod.ErrNoTenant) {
		t.Fatalf("create without tenant = %v, want ErrNoTenant", err)
	}
	if _, err := repo.Query().All(context.Background()); !errors.Is(err, tenantmod.ErrNoTenant) {
		t.Fatalf("query without tenant = %v, want ErrNoTenant", err)
	}
}

func TestMiddlewareResolvesHeader(t *testing.T) {
	m := &tenantmod.Middleware{Resolver: tenantmod.HeaderResolver{}}
	var got string
	h := m.Handle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tn, ok := tenantmod.FromContext(r.Context()); ok {
			got = tn.ID
		}
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set(tenantmod.Header, "acme")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got != "acme" {
		t.Fatalf("resolved tenant = %q, want acme", got)
	}
}

func TestUpdateCannotTakeAnotherTenantsRow(t *testing.T) {
	repo := newRepo(t)
	a := tenantmod.WithTenant(context.Background(), "A")
	b := tenantmod.WithTenant(context.Background(), "B")
	if err := repo.Create(b, &User{ID: 42, Name: "bob"}); err != nil {
		t.Fatal(err)
	}

	if err := repo.Update(a, &User{ID: 42, Name: "mallory"}); !errors.Is(err, repomod.ErrNotFound) {
		t.Fatalf("cross-tenant update = %v, want ErrNotFound", err)
	}
	u, err := repo.Get(b, 42)
	if err != nil {
		t.Fatalf("B lost its row: %v", err)
	}
	if u.Name != "bob" || u.TenantID != "B" {
		t.Fatalf("B's row changed: %+v", u)
	}

	// The owner can still update it.
	if err := repo.Update(b, &User{ID: 42, Name: "robert"}); err != nil {
		t.Fatalf("own update: %v", err)
	}
	if u, _ := repo.Get(b, 42); u == nil || u.Name != "robert" {
		t.Fatalf("own update not applied: %+v", u)
	}
}

type Account struct {
	ID       uint `gorm:"primaryKey"`
	TenantID uint
	Name     string
}

func TestUintTenantColumn(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&Account{}); err != nil {
		t.Fatal(err)
	}
	repo := tenantmod.Scope[Account](&gormrepomod.GormRepo[Account]{DB: db}, "tenant_id")
	ctx := tenantmod.WithTenant(context.Background(), "7")

	if err := repo.Create(ctx, &Account{ID: 1, Name: "a"}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, 1)
	if err != nil {
		t.Fatalf("get own: %v", err)
	}
	if got.TenantID != 7 {
		t.Fatalf("tenant stamped %d, want 7", got.TenantID)
	}
}

func TestMisconfiguredColumnFails(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&User{}); err != nil {
		t.Fatal(err)
	}
	repo := tenantmod.Scope[User](&gormrepomod.GormRepo[User]{DB: db}, "org_id")
	ctx := tenantmod.WithTenant(context.Background(), "A")

	if err := repo.Create(ctx, &User{ID: 1, Name: "x"}); err == nil {
		t.Fatal("create with unknown tenant column succeeded")
	}
	var n int64
	db.Model(&User{}).Count(&n)
	if n != 0 {
		t.Fatalf("untenanted rows inserted: %d", n)
	}
}
