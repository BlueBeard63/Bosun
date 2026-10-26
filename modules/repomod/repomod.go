// Package repomod is the driver-agnostic contract for a generic repository
// pattern on top of Bosun.
//
// Services depend on repomod.Repo[T] for an entity type; a driver module
// (e.g. github.com/bluebeard63/bosun/modules/gormrepomod) registers the
// concrete implementation. Hosts can swap implementations per entity by
// registering their own Repo[T] before app.Start() — DefaultBind in the
// driver yields to host registrations.
//
//	import "github.com/bluebeard63/bosun/modules/repomod"
//	import "github.com/bluebeard63/bosun/modules/gormrepomod"
//
//	type User struct { ID uint; Email string }
//	var _ = gormrepomod.For[User]()
//
//	type UsersController struct {
//	    Users repomod.Repo[User] // injected
//	}
package repomod

import (
	"context"
	"net/http"
)

// ErrNotFound is returned by Get and Query.One when a row does not exist.
// Drivers translate their own "not found" sentinels into this value so
// callers can use errors.Is(err, repomod.ErrNotFound) portably.
//
// It carries HTTP status 404: a typed handler that returns it, wrapped or
// not, answers 404 "not found" without mapping it. Override that per app
// with bosun.MapError.
var ErrNotFound error = notFoundError{}

type notFoundError struct{}

func (notFoundError) Error() string         { return "repo: not found" }
func (notFoundError) HTTPStatus() int       { return http.StatusNotFound }
func (notFoundError) PublicMessage() string { return "not found" }

// Query is a chainable read query builder. Builder methods (Where, Order,
// Limit, Offset) return a new Query; terminal methods (One, All, Count)
// execute the query against the underlying DB.
type Query[T any] interface {
	Where(query any, args ...any) Query[T]
	Order(spec string) Query[T]
	Limit(n int) Query[T]
	Offset(n int) Query[T]
	One(ctx context.Context) (*T, error)
	All(ctx context.Context) ([]T, error)
	Count(ctx context.Context) (int64, error)
}

// Repo is the per-entity repository contract. All methods take a context;
// drivers carry deadlines/cancellation into the underlying DB call.
//
// Tx runs fn inside a transaction; fn receives a derived ctx that the
// driver tags as transactional. Any Repo[T] method called with that ctx
// participates in the same transaction — including repos for other
// entity types backed by the same driver. Returning a non-nil error from
// fn rolls the transaction back.
type Repo[T any] interface {
	Get(ctx context.Context, id any) (*T, error)
	Create(ctx context.Context, entity *T) error
	Update(ctx context.Context, entity *T) error
	Delete(ctx context.Context, id any) error
	Query() Query[T]
	Tx(ctx context.Context, fn func(ctx context.Context) error) error
}
