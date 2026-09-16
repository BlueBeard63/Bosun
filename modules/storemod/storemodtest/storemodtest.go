// Package storemodtest is a reusable conformance suite for storemod.Store
// implementations. Every driver's test calls Run with a ready store.
package storemodtest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/bluebeard63/bosun/modules/storemod"
)

// Run exercises the Store contract: put/get round-trip, listing by prefix,
// not-found behavior, and delete.
func Run(t *testing.T, s storemod.Store) {
	t.Helper()
	ctx := context.Background()

	if err := s.Put(ctx, "docs/a.txt", bytes.NewReader([]byte("hello")), storemod.WithContentType("text/plain")); err != nil {
		t.Fatalf("put: %v", err)
	}
	rc, obj, err := s.Get(ctx, "docs/a.txt")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	data, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(data) != "hello" {
		t.Fatalf("get body = %q, want hello", data)
	}
	if obj.Size != 5 {
		t.Fatalf("size = %d, want 5", obj.Size)
	}
	if obj.ContentType != "text/plain" {
		t.Fatalf("content type = %q, want text/plain", obj.ContentType)
	}

	if err := s.Put(ctx, "docs/b.txt", bytes.NewReader([]byte("x"))); err != nil {
		t.Fatalf("put b: %v", err)
	}
	if err := s.Put(ctx, "other/c.txt", bytes.NewReader([]byte("y"))); err != nil {
		t.Fatalf("put c: %v", err)
	}
	objs, err := s.List(ctx, "docs/")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(objs) != 2 {
		t.Fatalf("list docs/ returned %d objects, want 2", len(objs))
	}

	if _, _, err := s.Get(ctx, "missing/x"); !errors.Is(err, storemod.ErrNotFound) {
		t.Fatalf("get missing = %v, want ErrNotFound", err)
	}

	if err := s.Delete(ctx, "docs/a.txt"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, _, err := s.Get(ctx, "docs/a.txt"); !errors.Is(err, storemod.ErrNotFound) {
		t.Fatalf("get after delete = %v, want ErrNotFound", err)
	}
}
