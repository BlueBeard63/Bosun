// Package storemodtest is a reusable conformance suite for storemod.Store
// implementations. Every driver's test calls Run with a ready store.
package storemodtest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"slices"
	"sort"
	"testing"

	"github.com/bluebeard63/bosun/modules/storemod"
)

// Run exercises the Store contract: put/get round-trip, recursive listing by
// prefix, not-found behavior, and delete.
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
	// Compare the media type only: WebDAV servers derive the type from the
	// file extension and add parameters (text/plain; charset=utf-8).
	if mt, _, _ := mime.ParseMediaType(obj.ContentType); mt != "text/plain" {
		t.Fatalf("content type = %q, want text/plain", obj.ContentType)
	}

	if err := s.Put(ctx, "docs/b.txt", bytes.NewReader([]byte("x"))); err != nil {
		t.Fatalf("put b: %v", err)
	}
	if err := s.Put(ctx, "other/c.txt", bytes.NewReader([]byte("y"))); err != nil {
		t.Fatalf("put c: %v", err)
	}
	if err := s.Put(ctx, "docs/sub/deep/d.txt", bytes.NewReader([]byte("z"))); err != nil {
		t.Fatalf("put d: %v", err)
	}
	// List matches by key prefix at any depth, including partial segments.
	for prefix, want := range map[string][]string{
		"docs/":     {"docs/a.txt", "docs/b.txt", "docs/sub/deep/d.txt"},
		"docs/s":    {"docs/sub/deep/d.txt"},
		"docs/sub/": {"docs/sub/deep/d.txt"},
		"missing/":  nil,
	} {
		objs, err := s.List(ctx, prefix)
		if err != nil {
			t.Fatalf("list %s: %v", prefix, err)
		}
		var got []string
		for _, o := range objs {
			got = append(got, o.Key)
		}
		sort.Strings(got)
		if !slices.Equal(got, want) {
			t.Fatalf("list %s = %v, want %v", prefix, got, want)
		}
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
