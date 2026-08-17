package storefsmod_test

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/amberstack/bosun/modules/storefsmod"
	"github.com/amberstack/bosun/modules/storemod"
	"github.com/amberstack/bosun/modules/storemod/storemodtest"
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
