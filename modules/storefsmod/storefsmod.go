// Package storefsmod is a local-filesystem driver for storemod.Store. Objects
// live under <Root>/obj and their metadata under <Root>/meta. It has no
// external dependencies, which makes it the default store for development and
// tests. Presign returns an HMAC-signed URL when a Secret is configured, for
// serving objects through your own handler; otherwise Presign is unsupported.
// Put replaces objects atomically through temp files named ".bosun-tmp-*",
// which List never returns.
//
//	var _ = storefsmod.For()
package storefsmod

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/modules/storemod"
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

// Put writes the object and its metadata. Each file is written to a temp
// file beside its target, synced and renamed over it, so a failed or
// cancelled upload leaves the previous object intact.
func (s *FSStore) Put(ctx context.Context, key string, r io.Reader, opts ...storemod.PutOption) error {
	cfg := storemod.ResolvePut(opts)
	if err := writeAtomic(ctx, s.objPath(key), r); err != nil {
		return err
	}
	b, _ := json.Marshal(meta{ContentType: cfg.ContentType, Meta: cfg.Meta})
	return writeAtomic(ctx, s.metaPath(key), bytes.NewReader(b))
}

// tmpPrefix names in-progress uploads; List skips files that start with it.
const tmpPrefix = ".bosun-tmp-"

// writeAtomic streams r into a temp file in dst's directory, syncs it and
// renames it over dst. On any error, including ctx cancellation, the temp
// file is removed and dst is untouched.
func writeAtomic(ctx context.Context, dst string, r io.Reader) (err error) {
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, tmpPrefix+"*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	}()
	if _, err = io.Copy(f, ctxReader{ctx, r}); err != nil {
		return err
	}
	if err = f.Chmod(0o644); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), dst)
}

// ctxReader stops a copy as soon as ctx is done.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
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

// testHookVisit, when set by tests, sees every path List visits.
var testHookVisit func(p string)

// List returns objects whose key begins with prefix, at any depth. The walk
// starts at the deepest directory the prefix covers and skips directories
// that cannot hold a match, and only matching objects are stat'ed and have
// their metadata read.
func (s *FSStore) List(ctx context.Context, prefix string) ([]storemod.Object, error) {
	root := filepath.Join(s.Opts.Root, "obj")
	start := root
	if i := strings.LastIndex(prefix, "/"); i > 0 {
		// A prefix whose directory part is not a clean key matches nothing
		// under that directory; walking from root keeps traversal impossible.
		if dir := prefix[:i]; cleanKey(dir) == dir && !strings.Contains(dir, `\`) {
			start = filepath.Join(root, filepath.FromSlash(dir))
		}
	}
	var out []storemod.Object
	err := filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if testHookVisit != nil {
			testHookVisit(p)
		}
		if p == start && d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if d.IsDir() {
			if dk := key + "/"; !strings.HasPrefix(dk, prefix) && !strings.HasPrefix(prefix, dk) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), tmpPrefix) || !strings.HasPrefix(key, prefix) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if os.IsNotExist(err) {
				return nil // removed during the walk
			}
			return err
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
