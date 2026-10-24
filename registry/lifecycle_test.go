package registry

import (
	"strings"
	"testing"
)

func TestShutdownKeepsInstancesResolvable(t *testing.T) {
	r := New()
	db := &FakeDB{}
	RegisterInstance[*FakeDB](r, db)
	if err := r.Shutdown(); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve[*FakeDB](r)
	if err != nil || got != db {
		t.Fatalf("Resolve after Shutdown = %v, %v; want the instance", got, err)
	}
	if !db.closed {
		t.Fatal("instance Close not called")
	}
}

func TestShutdownRefusesRebuild(t *testing.T) {
	r := New()
	builds := 0
	Register[*AuthService](r, func(*Registry) (*AuthService, error) {
		builds++
		return &AuthService{}, nil
	})
	auth := MustResolve[*AuthService](r)
	if err := r.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if !auth.closed {
		t.Fatal("service Close not called")
	}
	if _, err := Resolve[*AuthService](r); err == nil || !strings.Contains(err.Error(), "after Shutdown") {
		t.Fatalf("Resolve after Shutdown err = %v, want refusal", err)
	}
	if builds != 1 {
		t.Fatalf("constructor ran %d times, want 1", builds)
	}
}

func TestShutdownRunsOnce(t *testing.T) {
	r := New()
	closes := 0
	RegisterInstance[*countCloser](r, &countCloser{n: &closes})
	_ = r.Shutdown()
	_ = r.Shutdown()
	if closes != 1 {
		t.Fatalf("Close called %d times, want 1", closes)
	}
}

type countCloser struct{ n *int }

func (c *countCloser) Close() error { *c.n++; return nil }
