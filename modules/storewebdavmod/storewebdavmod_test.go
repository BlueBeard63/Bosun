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
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/bluebeard63/bosun/modules/storemod/storemodtest"
)

func TestParseMultistatus(t *testing.T) {
	body := []byte(`<?xml version="1.0"?>
<d:multistatus xmlns:d="DAV:">
  <d:response>
    <d:href>/dav/user/docs/</d:href>
    <d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop></d:propstat>
  </d:response>
  <d:response>
    <d:href>/dav/user/docs/a.txt</d:href>
    <d:propstat><d:prop>
      <d:getcontentlength>5</d:getcontentlength>
      <d:getcontenttype>text/plain</d:getcontenttype>
      <d:resourcetype/>
    </d:prop></d:propstat>
  </d:response>
  <d:response>
    <d:href>/dav/user/docs/b%20c.txt</d:href>
    <d:propstat><d:prop>
      <d:getcontentlength>1</d:getcontentlength>
      <d:resourcetype/>
    </d:prop></d:propstat>
  </d:response>
</d:multistatus>`)

	objs, err := parseMultistatus(body, "/dav/user", "docs/")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(objs) != 2 {
		t.Fatalf("got %d objects, want 2 (collection skipped)", len(objs))
	}
	byKey := map[string]int64{}
	for _, o := range objs {
		byKey[o.Key] = o.Size
	}
	if byKey["docs/a.txt"] != 5 {
		t.Fatalf("a.txt size = %d, want 5", byKey["docs/a.txt"])
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

// fakeDAV is a minimal WebDAV server keyed by the decoded request path. It
// fails any request whose key leaked into the query or fragment.
type fakeDAV struct {
	mu    sync.Mutex
	files map[string][]byte
}

func (f *fakeDAV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		http.Error(w, "unexpected query "+r.URL.RawQuery, http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	switch r.Method {
	case "MKCOL":
		w.WriteHeader(http.StatusCreated)
	case http.MethodPut:
		b, _ := io.ReadAll(r.Body)
		f.files[p] = b
		w.WriteHeader(http.StatusCreated)
	case http.MethodGet:
		b, ok := f.files[p]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	case http.MethodDelete:
		delete(f.files, p)
		w.WriteHeader(http.StatusNoContent)
	case "PROPFIND":
		var keys []string
		for k := range f.files {
			if strings.HasPrefix(k, p) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		w.WriteHeader(http.StatusMultiStatus)
		fmt.Fprint(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:">`)
		for _, k := range keys {
			href := (&url.URL{Path: k}).EscapedPath()
			fmt.Fprintf(w, `<d:response><d:href>%s</d:href><d:propstat><d:prop><d:getcontentlength>%d</d:getcontentlength><d:resourcetype/></d:prop></d:propstat></d:response>`, href, len(f.files[k]))
		}
		fmt.Fprint(w, `</d:multistatus>`)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func TestKeysAreEscaped(t *testing.T) {
	dav := &fakeDAV{files: map[string][]byte{}}
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
