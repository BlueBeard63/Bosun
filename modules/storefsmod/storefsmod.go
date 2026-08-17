// Package storefsmod is a local-filesystem driver for storemod.Store. Objects
// live under <Root>/obj and their metadata under <Root>/meta. It has no
// external dependencies, which makes it the default store for development and
// tests. Presign returns an HMAC-signed URL when a Secret is configured, for
// serving objects through your own handler; otherwise Presign is unsupported.
//
//	var _ = storefsmod.For()
package storefsmod

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/amberstack/bosun"
	"github.com/amberstack/bosun/modules/storemod"
)

// Options configures the filesystem store.
type Options struct {
	Root    string // base directory
	BaseURL string // used by Presign to build URLs
	Secret  string // HMAC key for Presign; empty disables presigning
}

var _ = bosun.Default[*Options](func() *Options { return &Options{Root: "./data"} })

// FSStore stores objects on the local filesystem.
type FSStore struct {
	Opts *Options // injected
}

var _ storemod.Store = (*FSStore)(nil)

// cleanKey removes any path traversal so a key can never escape the root.
func cleanKey(key string) string {
	return strings.TrimPrefix(path.Clean("/"+key), "/")
}

func (s *FSStore) objPath(key string) string {
	return filepath.Join(s.Opts.Root, "obj", filepath.FromSlash(cleanKey(key)))
}

func (s *FSStore) metaPath(key string) string {
	return filepath.Join(s.Opts.Root, "meta", filepath.FromSlash(cleanKey(key))+".json")
}

type meta struct {
	ContentType string            `json:"content_type,omitempty"`
	Meta        map[string]string `json:"meta,omitempty"`
}

// Put writes the object and its metadata.
func (s *FSStore) Put(ctx context.Context, key string, r io.Reader, opts ...storemod.PutOption) error {
	cfg := storemod.ResolvePut(opts)
	op := s.objPath(key)
	if err := os.MkdirAll(filepath.Dir(op), 0o755); err != nil {
		return err
	}
	f, err := os.Create(op)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	mp := s.metaPath(key)
	if err := os.MkdirAll(filepath.Dir(mp), 0o755); err != nil {
		return err
	}
	b, _ := json.Marshal(meta{ContentType: cfg.ContentType, Meta: cfg.Meta})
	return os.WriteFile(mp, b, 0o644)
}

// Get opens the object and returns its metadata.
func (s *FSStore) Get(ctx context.Context, key string) (io.ReadCloser, *storemod.Object, error) {
	op := s.objPath(key)
	f, err := os.Open(op)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, storemod.ErrNotFound
		}
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	obj := &storemod.Object{Key: cleanKey(key), Size: info.Size(), ModTime: info.ModTime(), ContentType: s.contentType(key)}
	return f, obj, nil
}

func (s *FSStore) contentType(key string) string {
	if b, err := os.ReadFile(s.metaPath(key)); err == nil {
		var m meta
		if json.Unmarshal(b, &m) == nil && m.ContentType != "" {
			return m.ContentType
		}
	}
	if ct := mime.TypeByExtension(filepath.Ext(key)); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// Delete removes the object and its metadata; a missing key is not an error.
func (s *FSStore) Delete(ctx context.Context, key string) error {
	if err := os.Remove(s.objPath(key)); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(s.metaPath(key)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// List returns objects whose key begins with prefix.
func (s *FSStore) List(ctx context.Context, prefix string) ([]storemod.Object, error) {
	root := filepath.Join(s.Opts.Root, "obj")
	var out []storemod.Object
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if !strings.HasPrefix(key, prefix) {
			return nil
		}
		out = append(out, storemod.Object{Key: key, Size: info.Size(), ModTime: info.ModTime(), ContentType: s.contentType(key)})
		return nil
	})
	return out, err
}

// Presign returns an HMAC-signed URL for the key. It requires Options.Secret
// and Options.BaseURL; without them it returns ErrUnsupported. Verify the URL
// in your download handler with Verify.
func (s *FSStore) Presign(ctx context.Context, key string, op storemod.Op, ttl time.Duration) (string, error) {
	if s.Opts.Secret == "" || s.Opts.BaseURL == "" {
		return "", storemod.ErrUnsupported
	}
	exp := time.Now().Add(ttl).Unix()
	key = cleanKey(key)
	sig := sign(s.Opts.Secret, key, op, exp)
	q := url.Values{"exp": {strconv.FormatInt(exp, 10)}, "op": {strconv.Itoa(int(op))}, "sig": {sig}}
	return strings.TrimRight(s.Opts.BaseURL, "/") + "/" + key + "?" + q.Encode(), nil
}

func sign(secret, key string, op storemod.Op, exp int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%s\n%d\n%d", key, int(op), exp)
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify reports whether a presigned request is valid and unexpired. Call it
// from the handler that serves presigned objects.
func Verify(secret, key string, op storemod.Op, exp int64, sig string) bool {
	if time.Now().Unix() > exp {
		return false
	}
	want := sign(secret, cleanKey(key), op, exp)
	return hmac.Equal([]byte(want), []byte(sig))
}

// For registers the filesystem store and binds storemod.Store to it.
func For() struct{} {
	bosun.Service[FSStore]()
	bosun.DefaultBind[storemod.Store, FSStore]()
	return struct{}{}
}
