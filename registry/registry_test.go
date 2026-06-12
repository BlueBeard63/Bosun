package registry

import (
	"fmt"
	"strings"
	"testing"
)

// --- stand-ins for real services (FakeDB plays the role of *gorm.DB) ---

type FakeDB struct{ closed bool }

func (db *FakeDB) Close() error { db.closed = true; return nil }

type UserService interface{ Name(id int) string }
type PasswordService interface{ Hash(pw string) string }

type userService struct{ db *FakeDB }

func (u *userService) Name(int) string { return "jack" }

type passwordService struct{ db *FakeDB }

func (p *passwordService) Hash(s string) string { return "h:" + s }

type AuthService struct {
	users     UserService
	passwords PasswordService
	closed    bool
}

func (a *AuthService) Close() error { a.closed = true; return nil }

func TestFullGraph(t *testing.T) {
	r := New()

	db := &FakeDB{}
	RegisterInstance[*FakeDB](r, db) // <- the gorm.DB pattern

	Register[UserService](r, func(r *Registry) (UserService, error) {
		db, err := Resolve[*FakeDB](r)
		if err != nil {
			return nil, err
		}
		return &userService{db: db}, nil
	})

	Register[PasswordService](r, func(r *Registry) (PasswordService, error) {
		db, err := Resolve[*FakeDB](r)
		if err != nil {
			return nil, err
		}
		return &passwordService{db: db}, nil
	})

	Register[*AuthService](r, func(r *Registry) (*AuthService, error) {
		users, err := Resolve[UserService](r)
		if err != nil {
			return nil, err
		}
		pws, err := Resolve[PasswordService](r)
		if err != nil {
			return nil, err
		}
		return &AuthService{users: users, passwords: pws}, nil
	})

	if err := r.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	auth := MustResolve[*AuthService](r)
	if auth.users.Name(1) != "jack" {
		t.Fatal("user service broken")
	}

	g := r.Graph()
	authDeps := g["*registry.AuthService"]
	if len(authDeps) != 2 {
		t.Fatalf("expected 2 deps on AuthService, got %v", authDeps)
	}
	fmt.Println("--- adjacency ---")
	for k, v := range g {
		fmt.Printf("%s -> %v\n", k, v)
	}
	fmt.Println("--- DOT ---")
	fmt.Print(r.DOT())

	if err := r.Shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if !auth.closed || !db.closed {
		t.Fatal("closers not invoked")
	}
}

func TestCycleDetection(t *testing.T) {
	type A struct{}
	type B struct{}
	r := New()
	Register[*A](r, func(r *Registry) (*A, error) {
		if _, err := Resolve[*B](r); err != nil {
			return nil, err
		}
		return &A{}, nil
	})
	Register[*B](r, func(r *Registry) (*B, error) {
		if _, err := Resolve[*A](r); err != nil {
			return nil, err
		}
		return &B{}, nil
	})
	err := r.Validate()
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected cycle error, got %v", err)
	}
	fmt.Println("cycle error message:", err)
}

func TestMissingProvider(t *testing.T) {
	r := New()
	Register[*AuthService](r, func(r *Registry) (*AuthService, error) {
		if _, err := Resolve[UserService](r); err != nil {
			return nil, err
		}
		return &AuthService{}, nil
	})
	err := r.Validate()
	if err == nil || !strings.Contains(err.Error(), "no provider") {
		t.Fatalf("expected missing provider error, got %v", err)
	}
	fmt.Println("missing provider message:", err)
}

func TestRefInject(t *testing.T) {
	type Deps struct {
		Users Ref[UserService]
	}
	r := New()
	Register[UserService](r, func(r *Registry) (UserService, error) {
		return &userService{}, nil
	})
	var d Deps
	if err := Inject(r, &d); err != nil {
		t.Fatal(err)
	}
	if d.Users.Get().Name(1) != "jack" {
		t.Fatal("ref injection failed")
	}
}
