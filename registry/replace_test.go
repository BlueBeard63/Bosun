package registry

import (
	"strings"
	"testing"
)

func mustPanic(t *testing.T, want string, f func()) {
	t.Helper()
	defer func() {
		t.Helper()
		r := recover()
		if r == nil {
			t.Fatalf("expected panic containing %q", want)
		}
		if s, _ := r.(string); !strings.Contains(s, want) {
			t.Fatalf("panic = %v; want it to contain %q", r, want)
		}
	}()
	f()
}

func TestReplaceConstructorIsInjectedIntoDependents(t *testing.T) {
	r := New()
	built := false
	Register[UserService](r, func(*Registry) (UserService, error) {
		built = true
		return &userService{}, nil
	})
	Register[*AuthService](r, func(r *Registry) (*AuthService, error) {
		u, err := Resolve[UserService](r)
		return &AuthService{users: u}, err
	})

	fake := &userService{db: &FakeDB{}}
	Replace[UserService](r, fake)

	auth, err := Resolve[*AuthService](r)
	if err != nil {
		t.Fatal(err)
	}
	if auth.users != fake {
		t.Fatalf("dependent got %v; want the replacement", auth.users)
	}
	if built {
		t.Fatal("replaced constructor ran")
	}
}

func TestReplaceRegistersWhenAbsent(t *testing.T) {
	r := New()
	db := &FakeDB{}
	Replace[*FakeDB](r, db)
	if got := MustResolve[*FakeDB](r); got != db {
		t.Fatalf("got %v; want the replacement", got)
	}
	// Absent-then-registered: a later Register still sees a duplicate.
	mustPanic(t, "duplicate provider", func() { RegisterInstance[*FakeDB](r, &FakeDB{}) })
}

func TestReplaceInstanceBeforeUse(t *testing.T) {
	r := New()
	RegisterInstance[*FakeDB](r, &FakeDB{})
	db := &FakeDB{}
	Replace[*FakeDB](r, db)
	Replace[*FakeDB](r, db) // idempotent while unused
	if got := MustResolve[*FakeDB](r); got != db {
		t.Fatalf("got %v; want the replacement", got)
	}
}

func TestReplaceClosedOnShutdown(t *testing.T) {
	r := New()
	Register[*FakeDB](r, func(*Registry) (*FakeDB, error) { return &FakeDB{}, nil })
	db := &FakeDB{}
	Replace[*FakeDB](r, db)
	if err := r.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if !db.closed {
		t.Fatal("replacement not closed on Shutdown")
	}
}

func TestReplaceRefusedAfterConstruction(t *testing.T) {
	r := New()
	Register[UserService](r, func(*Registry) (UserService, error) { return &userService{}, nil })
	MustResolve[UserService](r)
	mustPanic(t, "already been constructed", func() { Replace[UserService](r, &userService{}) })
}

func TestReplaceRefusedOnceADependentIsBuilt(t *testing.T) {
	r := New()
	RegisterInstance[*FakeDB](r, &FakeDB{})
	Register[UserService](r, func(r *Registry) (UserService, error) {
		db, err := Resolve[*FakeDB](r)
		return &userService{db: db}, err
	})
	MustResolve[UserService](r)
	mustPanic(t, "already depends on it", func() { Replace[*FakeDB](r, &FakeDB{}) })
}

func TestReplaceRefusedDuringConstruction(t *testing.T) {
	r := New()
	Register[UserService](r, func(r *Registry) (UserService, error) {
		Replace[UserService](r, &userService{})
		return &userService{}, nil
	})
	mustPanic(t, "already been constructed", func() { _, _ = Resolve[UserService](r) })
}

func TestReplaceRefusedAfterShutdown(t *testing.T) {
	r := New()
	RegisterInstance[*FakeDB](r, &FakeDB{})
	_ = r.Shutdown()
	mustPanic(t, "after Shutdown", func() { Replace[*FakeDB](r, &FakeDB{}) })
}
