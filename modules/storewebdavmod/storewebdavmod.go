// Package storewebdavmod is a WebDAV driver for storemod.Store, hand-rolled over
// net/http with no external dependencies. It works with WebDAV servers and
// NextCloud (which exposes files over WebDAV). List walks collections with
// Depth: 1 PROPFINDs, so it is recursive like the other drivers. Presign is
// unsupported, since generic WebDAV has no signed-URL mechanism.
//
//	var _ = storewebdavmod.For()
package storewebdavmod

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/modules/storemod"
)

// Options configures the WebDAV endpoint.
type Options struct {
	BaseURL  string // e.g. https://cloud.example/remote.php/dav/files/user
	Username string
	Password string
	HTTP     *http.Client // nil uses a client with DefaultTimeout
}

var _ = bosun.Default[*Options](func() *Options { return &Options{} })

// DefaultTimeout bounds each request when Options.HTTP is nil. It covers the
// whole exchange including the body, so it is generous enough for large
// objects; set Options.HTTP for a different policy.
const DefaultTimeout = 5 * time.Minute

var defaultClient = &http.Client{Timeout: DefaultTimeout}

// maxKnownDirs caps the cache of collections known to exist.
const maxKnownDirs = 4096

// WebDAVStore stores objects on a WebDAV server.
type WebDAVStore struct {
	Opts *Options // injected

	mu   sync.Mutex
	dirs map[string]bool // collections known to exist, so Put skips MKCOL
}

var _ storemod.Store = (*WebDAVStore)(nil)

func (s *WebDAVStore) client() *http.Client {
	if s.Opts.HTTP != nil {
		return s.Opts.HTTP
	}
	return defaultClient
}

// ErrInvalidKey is returned for keys that hide a path traversal or a
// separator behind percent-encoding, which some servers decode.
var ErrInvalidKey = errors.New("webdav: invalid key")

func cleanKey(key string) string { return strings.TrimPrefix(path.Clean("/"+key), "/") }

// checkKey rejects keys whose percent-decoded form has a "." or ".."
// segment or more separators than the key itself.
func checkKey(key string) error {
	dec, err := url.PathUnescape(key)
	if err != nil || dec == key {
		return nil // nothing decodes; a literal % is escaped on the wire
	}
	if strings.Count(dec, "/") != strings.Count(key, "/") || strings.Contains(dec, "\\") {
		return fmt.Errorf("%w: %q", ErrInvalidKey, key)
	}
	for _, seg := range strings.Split(dec, "/") {
		if seg == "." || seg == ".." {
			return fmt.Errorf("%w: %q", ErrInvalidKey, key)
		}
	}
	return nil
}

// urlFor joins the cleaned key onto BaseURL, escaping each segment so
// characters such as "?", "#" and "%" stay part of the key.
func (s *WebDAVStore) urlFor(key string) (string, error) {
	if err := checkKey(key); err != nil {
		return "", err
	}
	return s.join(cleanKey(key)), nil
}

func (s *WebDAVStore) join(key string) string {
	segs := strings.Split(key, "/")
	for i, seg := range segs {
		segs[i] = url.PathEscape(seg)
	}
	return strings.TrimRight(s.Opts.BaseURL, "/") + "/" + strings.Join(segs, "/")
}

func (s *WebDAVStore) do(ctx context.Context, method, u string, body io.Reader, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if s.Opts.Username != "" || s.Opts.Password != "" {
		req.SetBasicAuth(s.Opts.Username, s.Opts.Password)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return s.client().Do(req)
}

// Put uploads the object. When the body can be rewound (an io.Seeker) it
// PUTs first and creates parent collections only if the server answers 409
// or 404, then retries; otherwise it creates parents it has not seen before
// up front. Collections created or written into are remembered, so repeat
// Puts into the same folder cost a single request.
func (s *WebDAVStore) Put(ctx context.Context, key string, r io.Reader, opts ...storemod.PutOption) error {
	u, err := s.urlFor(key)
	if err != nil {
		return err
	}
	cfg := storemod.ResolvePut(opts)
	hdr := map[string]string{}
	if cfg.ContentType != "" {
		hdr["Content-Type"] = cfg.ContentType
	}
	dir := path.Dir(cleanKey(key))
	if dir == "." {
		dir = ""
	}
	seeker, rewind := r.(io.Seeker)
	var start int64
	if rewind {
		if start, err = seeker.Seek(0, io.SeekCurrent); err != nil {
			rewind = false
		}
	}
	if !rewind {
		s.mkcolAll(ctx, dir)
	}
	resp, err := s.put(ctx, u, r, hdr)
	if err != nil {
		return err
	}
	if (resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusNotFound) && dir != "" {
		s.forget(dir) // the server says a parent is missing
		if rewind {
			s.mkcolAll(ctx, dir)
			if _, err := seeker.Seek(start, io.SeekStart); err != nil {
				return err
			}
			if resp, err = s.put(ctx, u, r, hdr); err != nil {
				return err
			}
		}
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("webdav put %s: %s", key, resp.Status)
	}
	s.remember(dir)
	return nil
}

// put sends one PUT and closes the response body. The caller's reader is
// never closed, so a seekable body can be replayed.
func (s *WebDAVStore) put(ctx context.Context, u string, r io.Reader, hdr map[string]string) (*http.Response, error) {
	if _, ok := r.(io.Closer); ok {
		r = struct{ io.Reader }{r}
	}
	resp, err := s.do(ctx, http.MethodPut, u, r, hdr)
	if err != nil {
		return nil, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	_ = resp.Body.Close()
	return resp, nil
}

// mkcolAll creates dir, trying the deepest collection first and walking up
// only while the server answers 409 (parent missing). Errors are left for
// the following PUT to report.
func (s *WebDAVStore) mkcolAll(ctx context.Context, dir string) {
	if dir == "" || s.known(dir) {
		return
	}
	mkcol := func() int {
		resp, err := s.do(ctx, "MKCOL", s.join(dir), nil, nil)
		if err != nil {
			return 0
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	code := mkcol()
	if code == http.StatusConflict {
		if parent := path.Dir(dir); parent != "." {
			s.mkcolAll(ctx, parent)
		}
		code = mkcol()
	}
	// 405 Method Not Allowed means the collection already exists.
	if code == http.StatusCreated || code == http.StatusMethodNotAllowed {
		s.remember(dir)
	}
}

func (s *WebDAVStore) known(dir string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dirs[dir]
}

// remember records dir and its ancestors as existing.
func (s *WebDAVStore) remember(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dirs == nil || len(s.dirs) >= maxKnownDirs {
		s.dirs = map[string]bool{}
	}
	for ; dir != "" && dir != "."; dir = path.Dir(dir) {
		s.dirs[dir] = true
	}
}

// forget drops dir and its ancestors, any of which may have been removed.
func (s *WebDAVStore) forget(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ; dir != "" && dir != "."; dir = path.Dir(dir) {
		delete(s.dirs, dir)
	}
}

// Get downloads the object and its metadata.
func (s *WebDAVStore) Get(ctx context.Context, key string) (io.ReadCloser, *storemod.Object, error) {
	u, err := s.urlFor(key)
	if err != nil {
		return nil, nil, err
	}
	resp, err := s.do(ctx, http.MethodGet, u, nil, nil)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, nil, storemod.ErrNotFound
	}
	if resp.StatusCode/100 != 2 {
		_ = resp.Body.Close()
		return nil, nil, fmt.Errorf("webdav get %s: %s", key, resp.Status)
	}
	obj := &storemod.Object{Key: cleanKey(key), Size: resp.ContentLength, ContentType: resp.Header.Get("Content-Type")}
	if lm := resp.Header.Get("Last-Modified"); lm != "" {
		if t, err := http.ParseTime(lm); err == nil {
			obj.ModTime = t
		}
	}
	return resp.Body, obj, nil
}

// Delete removes the object; a missing key is not an error.
func (s *WebDAVStore) Delete(ctx context.Context, key string) error {
	u, err := s.urlFor(key)
	if err != nil {
		return err
	}
	resp, err := s.do(ctx, http.MethodDelete, u, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode/100 == 2 {
		return nil
	}
	return fmt.Errorf("webdav delete %s: %s", key, resp.Status)
}

// List returns objects whose key begins with prefix, at any depth, matching
// the other drivers. Depth: infinity is often disabled on servers, so it
// walks one collection at a time with Depth: 1 PROPFINDs, starting at the
// deepest collection the prefix covers and descending only into collections
// that can hold a match.
func (s *WebDAVStore) List(ctx context.Context, prefix string) ([]storemod.Object, error) {
	if err := checkKey(prefix); err != nil {
		return nil, err
	}
	basePath := "/"
	if u, err := url.Parse(s.Opts.BaseURL); err == nil {
		basePath = u.Path
	}
	start := ""
	if i := strings.LastIndex(prefix, "/"); i > 0 {
		start = cleanKey(prefix[:i])
	}
	var out []storemod.Object
	queue := []string{start}
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]
		err := s.propfind(ctx, dir, basePath, func(key string, isDir bool, obj storemod.Object) {
			// Only descend strictly below dir, so a server that echoes a
			// collection under another href cannot loop the walk.
			below := key != "" && (dir == "" || strings.HasPrefix(key, dir+"/"))
			switch {
			case isDir:
				if dk := key + "/"; below && (strings.HasPrefix(dk, prefix) || strings.HasPrefix(prefix, dk)) {
					queue = append(queue, key)
				}
			case key != "" && strings.HasPrefix(key, prefix):
				out = append(out, obj)
			}
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// propfind lists one collection with a Depth: 1 PROPFIND and streams each
// entry to fn. A missing collection lists as empty.
func (s *WebDAVStore) propfind(ctx context.Context, dir, basePath string, fn func(key string, isDir bool, obj storemod.Object)) error {
	u := s.join(dir)
	if dir != "" {
		u += "/" // collection URLs end in a slash; avoids a redirect
	}
	resp, err := s.do(ctx, "PROPFIND", u, strings.NewReader(propfindBody), map[string]string{"Depth": "1", "Content-Type": "application/xml"})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode != http.StatusMultiStatus && resp.StatusCode/100 != 2 {
		return fmt.Errorf("webdav propfind: %s", resp.Status)
	}
	return parseMultistatus(resp.Body, basePath, fn)
}

// Presign is unsupported for generic WebDAV.
func (s *WebDAVStore) Presign(ctx context.Context, key string, op storemod.Op, ttl time.Duration) (string, error) {
	return "", storemod.ErrUnsupported
}

const propfindBody = `<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop>` +
	`<d:getcontentlength/><d:getcontenttype/><d:getlastmodified/><d:resourcetype/></d:prop></d:propfind>`

type msResponse struct {
	Href     string `xml:"href"`
	Propstat []struct {
		Prop struct {
			ContentLength string    `xml:"getcontentlength"`
			ContentType   string    `xml:"getcontenttype"`
			LastModified  string    `xml:"getlastmodified"`
			Collection    *struct{} `xml:"resourcetype>collection"`
		} `xml:"prop"`
	} `xml:"propstat"`
}

// parseMultistatus decodes a PROPFIND reply one <response> at a time, so a
// large listing is never held in memory, and calls fn for each entry. Props
// are merged across propstat blocks, since servers put found and missing
// props in separate ones.
func parseMultistatus(r io.Reader, basePath string, fn func(key string, isDir bool, obj storemod.Object)) error {
	dec := xml.NewDecoder(r)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "response" {
			continue
		}
		var res msResponse
		if err := dec.DecodeElement(&res, &se); err != nil {
			return err
		}
		if len(res.Propstat) == 0 {
			continue
		}
		obj := storemod.Object{Key: hrefToKey(basePath, res.Href)}
		isDir := false
		for _, ps := range res.Propstat {
			p := ps.Prop
			if p.Collection != nil {
				isDir = true
			}
			if p.ContentType != "" {
				obj.ContentType = p.ContentType
			}
			if p.ContentLength != "" {
				obj.Size, _ = strconv.ParseInt(p.ContentLength, 10, 64)
			}
			if p.LastModified != "" {
				if t, err := http.ParseTime(p.LastModified); err == nil {
					obj.ModTime = t
				}
			}
		}
		fn(obj.Key, isDir, obj)
	}
}

func hrefToKey(basePath, href string) string {
	p := href
	if u, err := url.Parse(href); err == nil {
		p = u.Path
	}
	if dec, err := url.PathUnescape(p); err == nil {
		p = dec
	}
	p = strings.TrimPrefix(p, strings.TrimRight(basePath, "/"))
	return strings.Trim(p, "/")
}

// For registers the WebDAV store and binds storemod.Store to it.
func For() struct{} {
	bosun.Service[WebDAVStore]()
	bosun.DefaultBind[storemod.Store, WebDAVStore]()
	return struct{}{}
}
