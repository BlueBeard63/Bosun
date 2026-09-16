// Package gormrepomod is a GORM-backed driver for repomod.Repo[T].
//
// Single-DB apps register *gorm.DB on the registry and declare repos
// with For[T]:
//
//	registry.RegisterInstance[*gorm.DB](app.Reg, db)
//	var _ = gormrepomod.For[User]()
//
// Multi-DB apps declare named DB types (a struct wrapping *gorm.DB that
// implements GormDB) and use Named[T, DB] to bind a repo to a specific
// database:
//
//	type PrimaryDB struct{ *gorm.DB }
//	func (p *PrimaryDB) Unwrap() *gorm.DB { return p.DB }
//
//	registry.RegisterInstance[*PrimaryDB](app.Reg, &PrimaryDB{DB: pdb})
//	registry.RegisterInstance[*AnalyticsDB](app.Reg, &AnalyticsDB{DB: adb})
//
//	var _ = gormrepomod.Named[User, *PrimaryDB]()
//	var _ = gormrepomod.Named[Event, *AnalyticsDB]()
package gormrepomod

import (
	"context"
	"errors"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/modules/repomod"
	"gorm.io/gorm"
)

// GormDB is the contract that named-DB wrapper types must satisfy. The
// canonical shape is `type PrimaryDB struct{ *gorm.DB }` with an Unwrap
// method returning the embedded pointer.
type GormDB interface {
	Unwrap() *gorm.DB
}

// txKey tags a context that is inside a Repo.Tx callback. The value is
// the active *gorm.DB transaction handle.
type txKey struct{}

// session returns the DB handle the next operation should run against.
// Inside a Tx block the stashed *gorm.DB (the transaction) is used as-is,
// so participation propagates across any number of repos. Outside a Tx,
// db.WithContext(ctx) attaches the request context for cancellation.
func session(db *gorm.DB, ctx context.Context) *gorm.DB {
	if tx, ok := ctx.Value(txKey{}).(*gorm.DB); ok {
		return tx
	}
	return db.WithContext(ctx)
}

// --- shared CRUD helpers used by both GormRepo and GormRepoWith ---

func crudGet[T any](db *gorm.DB, id any) (*T, error) {
	var v T
	if err := db.First(&v, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, repomod.ErrNotFound
		}
		return nil, err
	}
	return &v, nil
}

func crudCreate[T any](db *gorm.DB, e *T) error { return db.Create(e).Error }
func crudUpdate[T any](db *gorm.DB, e *T) error { return db.Save(e).Error }

func crudDelete[T any](db *gorm.DB, id any) error {
	var zero T
	return db.Delete(&zero, id).Error
}

func runTx(db *gorm.DB, ctx context.Context, fn func(context.Context) error) error {
	if _, ok := ctx.Value(txKey{}).(*gorm.DB); ok {
		// already inside a transaction — reuse it
		return fn(ctx)
	}
	return db.Transaction(func(tx *gorm.DB) error {
		return fn(context.WithValue(ctx, txKey{}, tx))
	})
}

// --- GormRepo: default *gorm.DB ---

// GormRepo is the default-DB implementation. It is auto-registered by For[T];
// hosts should not construct it directly.
type GormRepo[T any] struct {
	DB *gorm.DB // injected from the default *gorm.DB on the registry
}

func (r *GormRepo[T]) Get(ctx context.Context, id any) (*T, error) {
	return crudGet[T](session(r.DB, ctx), id)
}
func (r *GormRepo[T]) Create(ctx context.Context, e *T) error {
	return crudCreate(session(r.DB, ctx), e)
}
func (r *GormRepo[T]) Update(ctx context.Context, e *T) error {
	return crudUpdate(session(r.DB, ctx), e)
}
func (r *GormRepo[T]) Delete(ctx context.Context, id any) error {
	return crudDelete[T](session(r.DB, ctx), id)
}
func (r *GormRepo[T]) Query() repomod.Query[T] {
	return &gormQuery[T]{base: r.DB}
}
func (r *GormRepo[T]) Tx(ctx context.Context, fn func(context.Context) error) error {
	return runTx(r.DB, ctx, fn)
}

// --- GormRepoWith: named DB type ---

// GormRepoWith is the named-DB implementation. It is auto-registered by
// Named[T, DB].
type GormRepoWith[T any, DB GormDB] struct {
	Source DB // injected from the registered named DB instance
}

func (r *GormRepoWith[T, DB]) base() *gorm.DB { return r.Source.Unwrap() }

func (r *GormRepoWith[T, DB]) Get(ctx context.Context, id any) (*T, error) {
	return crudGet[T](session(r.base(), ctx), id)
}
func (r *GormRepoWith[T, DB]) Create(ctx context.Context, e *T) error {
	return crudCreate(session(r.base(), ctx), e)
}
func (r *GormRepoWith[T, DB]) Update(ctx context.Context, e *T) error {
	return crudUpdate(session(r.base(), ctx), e)
}
func (r *GormRepoWith[T, DB]) Delete(ctx context.Context, id any) error {
	return crudDelete[T](session(r.base(), ctx), id)
}
func (r *GormRepoWith[T, DB]) Query() repomod.Query[T] {
	return &gormQuery[T]{base: r.base()}
}
func (r *GormRepoWith[T, DB]) Tx(ctx context.Context, fn func(context.Context) error) error {
	return runTx(r.base(), ctx, fn)
}

// --- query builder ---

// gormQuery is a stateless chainable that records query options and runs
// them against a fresh GORM session at terminal time. A new gormQuery is
// returned from every builder call so chains compose without aliasing.
type gormQuery[T any] struct {
	base   *gorm.DB
	wheres []whereClause
	order  string
	limit  int
	offset int
}

type whereClause struct {
	query any
	args  []any
}

func (q *gormQuery[T]) clone() *gormQuery[T] {
	out := *q
	out.wheres = append([]whereClause(nil), q.wheres...)
	return &out
}

func (q *gormQuery[T]) Where(query any, args ...any) repomod.Query[T] {
	out := q.clone()
	out.wheres = append(out.wheres, whereClause{query: query, args: args})
	return out
}
func (q *gormQuery[T]) Order(spec string) repomod.Query[T] {
	out := q.clone()
	out.order = spec
	return out
}
func (q *gormQuery[T]) Limit(n int) repomod.Query[T] {
	out := q.clone()
	out.limit = n
	return out
}
func (q *gormQuery[T]) Offset(n int) repomod.Query[T] {
	out := q.clone()
	out.offset = n
	return out
}

func (q *gormQuery[T]) build(ctx context.Context) *gorm.DB {
	tx := session(q.base, ctx)
	for _, w := range q.wheres {
		tx = tx.Where(w.query, w.args...)
	}
	if q.order != "" {
		tx = tx.Order(q.order)
	}
	if q.limit > 0 {
		tx = tx.Limit(q.limit)
	}
	if q.offset > 0 {
		tx = tx.Offset(q.offset)
	}
	return tx
}

func (q *gormQuery[T]) One(ctx context.Context) (*T, error) {
	var v T
	if err := q.build(ctx).First(&v).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, repomod.ErrNotFound
		}
		return nil, err
	}
	return &v, nil
}

func (q *gormQuery[T]) All(ctx context.Context) ([]T, error) {
	var out []T
	return out, q.build(ctx).Find(&out).Error
}

func (q *gormQuery[T]) Count(ctx context.Context) (int64, error) {
	var n int64
	var zero T
	return n, q.build(ctx).Model(&zero).Count(&n).Error
}

// --- registration helpers ---

// For registers *GormRepo[T] as an injectable singleton and binds
// repomod.Repo[T] to it. The host's default *gorm.DB is used.
func For[T any]() struct{} {
	bosun.Service[GormRepo[T]]()
	bosun.DefaultBind[repomod.Repo[T], GormRepo[T]]()
	return struct{}{}
}

// Named registers a *GormRepoWith[T, DB] as an injectable singleton and binds
// repomod.Repo[T] to it. The host must have registered an instance of DB
// (a named pointer type implementing GormDB) on app.Reg.
func Named[T any, DB GormDB]() struct{} {
	bosun.Service[GormRepoWith[T, DB]]()
	bosun.DefaultBind[repomod.Repo[T], GormRepoWith[T, DB]]()
	return struct{}{}
}
