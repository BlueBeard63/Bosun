package storewebdavmod

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/bluebeard63/bosun/modules/storefsmod"
	"github.com/bluebeard63/bosun/modules/storemod"
	"github.com/bluebeard63/bosun/modules/storemod/storemodtest"
)

func TestParseMultistatus(t *testing.T) {
	body := strings.NewReader(`<?xml version="1.0"?>
<d:multistatus xmlns:d="DAV:">
  <d:response>
    <d:href>/dav/user/docs/</d:href>
    <d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop></d:propstat>
  </d:response>
  <d:response>
    <d:href>/dav/user/docs/a.txt</d:href>
    <d:propstat><d:prop>
      <d:getcontentlength>5</d:getcontentlength>
      <d:resourcetype/>
    </d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>
    <d:propstat><d:prop><d:getlastmodified/></d:prop><d:status>HTTP/1.1 404 Not Found</d:status></d:propstat>
    <d:propstat><d:prop>
      <d:getcontenttype>text/plain</d:getcontenttype>
    </d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>
  </d:response>
  <d:response>
    <d:href>/dav/user/docs/b%20c.txt</d:href>
    <d:propstat><d:prop>
      <d:getcontentlength>1</d:getcontentlength>
      <d:resourcetype/>
    </d:prop></d:propstat>
  </d:response>
</d:multistatus>`)

	var dirs []string
	byKey := map[string]storemod.Object{}
	err := parseMultistatus(body, "/dav/user", func(key string, isDir bool, obj storemod.Object) {
		if isDir {
			dirs = append(dirs, key)
			return
		}
		byKey[key] = obj
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(dirs) != 1 || dirs[0] != "docs" {
		t.Fatalf("collections = %v, want [docs]", dirs)
	}
	if len(byKey) != 2 {
		t.Fatalf("got %d objects, want 2", len(byKey))
	}
	if a := byKey["docs/a.txt"]; a.Size != 5 || a.ContentType != "text/plain" {
		t.Fatalf("a.txt = %+v, want size 5 and text/plain merged across propstats", a)
	}
	if _, ok := byKey["docs/b c.txt"]; !ok {
		t.Fatalf("expected url-decoded key 'docs/b c.txt', got keys %v", byKey)
	}
}

// Integration test runs only when BOSUN_WEBDAV_URL is set.
func TestConformance(t *testing.T) {
	base := os.Getenv("BOSUN_WEBDAV_URL")
	if base == "" {
		t.Skip("set BOSUN_WEBDAV_URL to run the WebDAV integration test")
	}
	s := &WebDAVStore{Opts: &Options{
		BaseURL:  base,
		Username: os.Getenv("BOSUN_WEBDAV_USER"),
		Password: os.Getenv("BOSUN_WEBDAV_PASS"),
	}}
	// clear any leftovers from a prior run
	_ = s.Delete(context.Background(), "docs/a.txt")
	storemodtest.Run(t, s)
}

// fakeDAV is a minimal WebDAV server keyed by the decoded request path. Like
// a real server it needs parent collections to exist (409 otherwise) and,
// like many, it refuses Depth: infinity PROPFINDs. It fails any request
// whose key leaked into the query or fragment, and counts requests by method.
type fakeDAV struct {
	mu    sync.Mutex
	files map[string][]byte
	types map[string]string
	dirs  map[string]bool
	calls map[string]int
}

func newFakeDAV(root string) *fakeDAV {
	return &fakeDAV{files: map[string][]byte{}, types: map[string]string{}, dirs: map[string]bool{root: true}, calls: map[string]int{}}
}

func (f *fakeDAV) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[method]
}

func (f *fakeDAV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		http.Error(w, "unexpected query "+r.URL.RawQuery, http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[r.Method]++
	p := strings.TrimSuffix(r.URL.Path, "/")
	switch r.Method {
	case "MKCOL":
		switch {
		case f.dirs[p] || f.files[p] != nil:
			w.WriteHeader(http.StatusMethodNotAllowed)
		case !f.dirs[path.Dir(p)]:
			w.WriteHeader(http.StatusConflict)
		default:
			f.dirs[p] = true
			w.WriteHeader(http.StatusCreated)
		}
	case http.MethodPut:
		b, _ := io.ReadAll(r.Body)
		if !f.dirs[path.Dir(p)] {
			w.WriteHeader(http.StatusConflict)
			return
		}
		f.files[p] = b
		f.types[p] = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusCreated)
	case http.MethodGet:
		b, ok := f.files[p]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if ct := f.types[p]; ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		_, _ = w.Write(b)
	case http.MethodDelete:
		delete(f.files, p)
		w.WriteHeader(http.StatusNoContent)
	case "PROPFIND":
		if r.Header.Get("Depth") != "1" {
			http.Error(w, "depth infinity disabled", http.StatusForbidden)
			return
		}
		if !f.dirs[p] {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusMultiStatus)
		fmt.Fprint(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:">`)
		writeDir := func(d string) {
			href := (&url.URL{Path: d + "/"}).EscapedPath()
			fmt.Fprintf(w, `<d:response><d:href>%s</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop></d:propstat></d:response>`, href)
		}
		writeDir(p)
		var dirs, files []string
		for d := range f.dirs {
			if d != p && path.Dir(d) == p {
				dirs = append(dirs, d)
			}
		}
		for k := range f.files {
			if path.Dir(k) == p {
				files = append(files, k)
			}
		}
		sort.Strings(dirs)
		sort.Strings(files)
		for _, d := range dirs {
			writeDir(d)
		}
		for _, k := range files {
			href := (&url.URL{Path: k}).EscapedPath()
			fmt.Fprintf(w, `<d:response><d:href>%s</d:href><d:propstat><d:prop><d:getcontentlength>%d</d:getcontentlength><d:resourcetype/></d:prop></d:propstat></d:response>`, href, len(f.files[k]))
		}
		fmt.Fprint(w, `</d:multistatus>`)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func newFakeStore(t *testing.T) (*WebDAVStore, *fakeDAV) {
	dav := newFakeDAV("/dav")
	srv := httptest.NewServer(dav)
	t.Cleanup(srv.Close)
	return &WebDAVStore{Opts: &Options{BaseURL: srv.URL + "/dav"}}, dav
}

func TestConformanceFake(t *testing.T) {
	s, _ := newFakeStore(t)
	storemodtest.Run(t, s)
}

// TestListMatchesFS checks WebDAV lists the same keys as the filesystem
// driver for the same nested layout.
func TestListMatchesFS(t *testing.T) {
	ctx := context.Background()
	dav, _ := newFakeStore(t)
	fsStore := &storefsmod.FSStore{Opts: &storefsmod.Options{Root: t.TempDir()}}
	for _, k := range []string{"top.txt", "a/1.txt", "a/b/2.txt", "a/b/c/3.txt", "a/bc/4.txt", "a/b c/5.txt", "z/6.txt"} {
		for _, s := range []storemod.Store{dav, fsStore} {
			if err := s.Put(ctx, k, strings.NewReader(k)); err != nil {
				t.Fatalf("put %s: %v", k, err)
			}
		}
	}
	keys := func(s storemod.Store, prefix string) []string {
		objs, err := s.List(ctx, prefix)
		if err != nil {
			t.Fatalf("list %q: %v", prefix, err)
		}
		var out []string
		for _, o := range objs {
			out = append(out, o.Key)
		}
		sort.Strings(out)
		return out
	}
	for _, prefix := range []string{"", "a", "a/", "a/b", "a/b/", "a/b/c/", "a/b ", "t", "missing/", "a/b/c/3.txt"} {
		got, want := keys(dav, prefix), keys(fsStore, prefix)
		if !slices.Equal(got, want) {
			t.Fatalf("list %q: webdav %v, fs %v", prefix, got, want)
		}
	}
	if got := keys(dav, ""); len(got) != 7 {
		t.Fatalf("list all = %v, want all 7 keys", got)
	}
}

func TestPutSkipsMkcolForKnownCollections(t *testing.T) {
	ctx := context.Background()
	s, dav := newFakeStore(t)
	if err := s.Put(ctx, "x/y/1.txt", strings.NewReader("1")); err != nil {
		t.Fatal(err)
	}
	mkcols := dav.count("MKCOL")
	if mkcols == 0 {
		t.Fatal("expected MKCOL for a new folder")
	}
	if err := s.Put(ctx, "x/y/2.txt", strings.NewReader("2")); err != nil {
		t.Fatal(err)
	}
	// A body that cannot be rewound still skips MKCOL for a known folder.
	if err := s.Put(ctx, "x/y/3.txt", io.MultiReader(strings.NewReader("3"))); err != nil {
		t.Fatal(err)
	}
	if got := dav.count("MKCOL"); got != mkcols {
		t.Fatalf("MKCOL count went %d -> %d on puts into a known folder", mkcols, got)
	}

	// A fresh store PUTs first, so an existing folder costs no MKCOL.
	fresh := &WebDAVStore{Opts: s.Opts}
	if err := fresh.Put(ctx, "x/y/4.txt", strings.NewReader("4")); err != nil {
		t.Fatal(err)
	}
	if got := dav.count("MKCOL"); got != mkcols {
		t.Fatalf("fresh store sent MKCOL for an existing folder (%d -> %d)", mkcols, got)
	}

	// A body that cannot be rewound into a new folder creates it up front.
	if err := fresh.Put(ctx, "p/q/5.txt", io.MultiReader(strings.NewReader("5"))); err != nil {
		t.Fatal(err)
	}
	rc, _, err := fresh.Get(ctx, "p/q/5.txt")
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
}

func TestDefaultClientHasTimeout(t *testing.T) {
	s := &WebDAVStore{Opts: &Options{}}
	if s.client().Timeout <= 0 {
		t.Fatal("default client has no timeout")
	}
	custom := &http.Client{}
	s.Opts.HTTP = custom
	if s.client() != custom {
		t.Fatal("Options.HTTP not used")
	}
}

func TestKeysAreEscaped(t *testing.T) {
	dav := newFakeDAV("/dav")
	srv := httptest.NewServer(dav)
	defer srv.Close()
	s := &WebDAVStore{Opts: &Options{BaseURL: srv.URL + "/dav"}}
	ctx := context.Background()

	keys := []string{"report", "report?v=2", "a#b", "a", "with space.txt", "100%.txt", "dir/x?y"}
	for _, k := range keys {
		if err := s.Put(ctx, k, strings.NewReader("body:"+k)); err != nil {
			t.Fatalf("Put(%q): %v", k, err)
		}
	}
	for _, k := range keys {
		rc, _, err := s.Get(ctx, k)
		if err != nil {
			t.Fatalf("Get(%q): %v", k, err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		if string(b) != "body:"+k {
			t.Fatalf("Get(%q) = %q, another key overwrote it", k, b)
		}
	}

	objs, err := s.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, o := range objs {
		got[o.Key] = true
	}
	for _, k := range keys {
		if !got[k] {
			t.Fatalf("List missing %q; got %v", k, got)
		}
	}
}

func TestEncodedTraversalRejected(t *testing.T) {
	s := &WebDAVStore{Opts: &Options{BaseURL: "http://127.0.0.1:1/dav"}}
	ctx := context.Background()
	for _, k := range []string{"%2e%2e/x", "a/%2E%2E/b", "a%2fb", "%2e/x"} {
		if err := s.Put(ctx, k, strings.NewReader("x")); !errors.Is(err, ErrInvalidKey) {
			t.Fatalf("Put(%q) = %v, want ErrInvalidKey", k, err)
		}
		if _, _, err := s.Get(ctx, k); !errors.Is(err, ErrInvalidKey) {
			t.Fatalf("Get(%q) = %v, want ErrInvalidKey", k, err)
		}
	}
}
