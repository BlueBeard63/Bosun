package repomod_test

import (
	"context"
	"errors"
	"testing"

	"github.com/amberstack/bosun/modules/repomod"
)

// stubRepo is a hand-written test fake; the existence of this file
// demonstrates that Repo[T] is satisfiable without any driver.
type stubRepo[T any] struct {
	rows  map[any]*T
	idGen func(*T) any
}

func newStub[T any](idGen func(*T) any) *stubRepo[T] {
	return &stubRepo[T]{rows: map[any]*T{}, idGen: idGen}
}

func (s *stubRepo[T]) Get(_ context.Context, id any) (*T, error) {
	v, ok := s.rows[id]
	if !ok {
		return nil, repomod.ErrNotFound
	}
	return v, nil
}
func (s *stubRepo[T]) Create(_ context.Context, e *T) error { s.rows[s.idGen(e)] = e; return nil }
func (s *stubRepo[T]) Update(_ context.Context, e *T) error { s.rows[s.idGen(e)] = e; return nil }
func (s *stubRepo[T]) Delete(_ context.Context, id any) error {
	delete(s.rows, id)
	return nil
}
func (s *stubRepo[T]) Query() repomod.Query[T] { panic("not implemented in stub") }
func (s *stubRepo[T]) Tx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

// Compile-time check: stubRepo satisfies Repo[T].
var _ repomod.Repo[struct{}] = (*stubRepo[struct{}])(nil)

type item struct {
	ID   int
	Name string
}

func TestStubRepo_RoundTrip(t *testing.T) {
	r := newStub[item](func(i *item) any { return i.ID })
	ctx := context.Background()

	if err := r.Create(ctx, &item{ID: 1, Name: "alpha"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := r.Get(ctx, 1)
	if err != nil || got.Name != "alpha" {
		t.Fatalf("get: got=%v err=%v", got, err)
	}
	if _, err := r.Get(ctx, 99); !errors.Is(err, repomod.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing id, got %v", err)
	}
}
