package storefsmod_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bluebeard63/bosun/modules/storefsmod"
	"github.com/bluebeard63/bosun/modules/storemod"
	"github.com/bluebeard63/bosun/modules/storemod/storemodtest"
)

func newStore(t *testing.T, opts *storefsmod.Options) *storefsmod.FSStore {
	if opts.Root == "" {
		opts.Root = t.TempDir()
	}
	return &storefsmod.FSStore{Opts: opts}
}

func TestConformance(t *testing.T) {
	storemodtest.Run(t, newStore(t, &storefsmod.Options{}))
}

func TestPresignAndVerify(t *testing.T) {
	s := newStore(t, &storefsmod.Options{BaseURL: "https://cdn.example", Secret: "sekret"})
	u, err := s.Presign(context.Background(), "a/b.txt", storemod.OpGet, time.Minute)
	if err != nil {
		t.Fatalf("presign: %v", err)
	}
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	exp, _ := strconv.ParseInt(parsed.Query().Get("exp"), 10, 64)
	sig := parsed.Query().Get("sig")

	if !storefsmod.Verify("sekret", "a/b.txt", storemod.OpGet, exp, sig) {
		t.Fatal("valid signature failed to verify")
	}
	if storefsmod.Verify("wrong-secret", "a/b.txt", storemod.OpGet, exp, sig) {
		t.Fatal("wrong secret verified")
	}
	if storefsmod.Verify("sekret", "a/b.txt", storemod.OpPut, exp, sig) {
		t.Fatal("wrong operation verified")
	}
}

func TestPresignUnsupportedWithoutSecret(t *testing.T) {
	s := newStore(t, &storefsmod.Options{})
	if _, err := s.Presign(context.Background(), "k", storemod.OpGet, time.Minute); !errors.Is(err, storemod.ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
}

// failingReader yields data once and then fails, like a client that
// disconnects midway through an upload. With cancel set it cancels instead.
type failingReader struct {
	data   string
	cancel context.CancelFunc
	done   bool
}

func (r *failingReader) Read(p []byte) (int, error) {
	if !r.done {
		r.done = true
		return copy(p, r.data), nil
	}
	if r.cancel != nil {
		r.cancel()
		return copy(p, "more"), nil
	}
	return 0, errors.New("client went away")
}

func getString(t *testing.T, s storemod.Store, key string) string {
	t.Helper()
	rc, _, err := s.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// assertNoTemp fails if an in-progress upload file is left under root.
func assertNoTemp(t *testing.T, root string) {
	t.Helper()
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(d.Name(), ".bosun-tmp-") {
			t.Errorf("temp file left behind: %s", p)
		}
		return nil
	})
}

func TestPutFailureKeepsPreviousObject(t *testing.T) {
	s := newStore(t, &storefsmod.Options{})
	ctx := context.Background()
	if err := s.Put(ctx, "img/avatar.png", strings.NewReader("old good object")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "img/avatar.png", &failingReader{data: "partial"}); err == nil {
		t.Fatal("put with failing reader succeeded")
	}
	if got := getString(t, s, "img/avatar.png"); got != "old good object" {
		t.Fatalf("object = %q after failed put, want the previous object", got)
	}
	assertNoTemp(t, s.Opts.Root)
}

func TestPutCancelledStopsAndCleansUp(t *testing.T) {
	s := newStore(t, &storefsmod.Options{})
	if err := s.Put(context.Background(), "a/b.txt", strings.NewReader("old")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := s.Put(ctx, "a/b.txt", &failingReader{data: "new", cancel: cancel})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("put = %v, want context.Canceled", err)
	}
	if got := getString(t, s, "a/b.txt"); got != "old" {
		t.Fatalf("object = %q after cancelled put, want old", got)
	}
	assertNoTemp(t, s.Opts.Root)

	if err := s.Put(ctx, "a/c.txt", strings.NewReader("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("put with done ctx = %v, want context.Canceled", err)
	}
	if _, _, err := s.Get(context.Background(), "a/c.txt"); !errors.Is(err, storemod.ErrNotFound) {
		t.Fatalf("get after cancelled put = %v, want ErrNotFound", err)
	}
}

func TestListSkipsTempFiles(t *testing.T) {
	s := newStore(t, &storefsmod.Options{})
	ctx := context.Background()
	if err := s.Put(ctx, "a/b.txt", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(s.Opts.Root, "obj", "a", ".bosun-tmp-123")
	if err := os.WriteFile(stray, []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}
	objs, err := s.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 1 || objs[0].Key != "a/b.txt" {
		t.Fatalf("list = %+v, want only a/b.txt", objs)
	}
}
