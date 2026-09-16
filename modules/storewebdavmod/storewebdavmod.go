// Package storewebdavmod is a WebDAV driver for storemod.Store, hand-rolled over
// net/http with no external dependencies. It works with WebDAV servers and
// NextCloud (which exposes files over WebDAV). Listing uses PROPFIND. Presign is
// unsupported, since generic WebDAV has no signed-URL mechanism.
//
//	var _ = storewebdavmod.For()
package storewebdavmod

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/modules/storemod"
)

// Options configures the WebDAV endpoint.
type Options struct {
	BaseURL  string // e.g. https://cloud.example/remote.php/dav/files/user
	Username string
	Password string
	HTTP     *http.Client
}

var _ = bosun.Default[*Options](func() *Options { return &Options{} })

// WebDAVStore stores objects on a WebDAV server.
type WebDAVStore struct {
	Opts *Options // injected
}

var _ storemod.Store = (*WebDAVStore)(nil)

func (s *WebDAVStore) client() *http.Client {
	if s.Opts.HTTP != nil {
		return s.Opts.HTTP
	}
	return http.DefaultClient
}

func cleanKey(key string) string { return strings.TrimPrefix(path.Clean("/"+key), "/") }

func (s *WebDAVStore) urlFor(key string) string {
	return strings.TrimRight(s.Opts.BaseURL, "/") + "/" + cleanKey(key)
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

// Put uploads the object, creating parent collections first.
func (s *WebDAVStore) Put(ctx context.Context, key string, r io.Reader, opts ...storemod.PutOption) error {
	cfg := storemod.ResolvePut(opts)
	s.mkcolParents(ctx, cleanKey(key))
	hdr := map[string]string{}
	if cfg.ContentType != "" {
		hdr["Content-Type"] = cfg.ContentType
	}
	resp, err := s.do(ctx, http.MethodPut, s.urlFor(key), r, hdr)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("webdav put %s: %s", key, resp.Status)
	}
	return nil
}

func (s *WebDAVStore) mkcolParents(ctx context.Context, key string) {
	parts := strings.Split(key, "/")
	if len(parts) < 2 {
		return
	}
	dir := ""
	for _, p := range parts[:len(parts)-1] {
		dir = path.Join(dir, p)
		resp, err := s.do(ctx, "MKCOL", strings.TrimRight(s.Opts.BaseURL, "/")+"/"+dir, nil, nil)
		if err == nil {
			_ = resp.Body.Close()
		}
	}
}

// Get downloads the object and its metadata.
func (s *WebDAVStore) Get(ctx context.Context, key string) (io.ReadCloser, *storemod.Object, error) {
	resp, err := s.do(ctx, http.MethodGet, s.urlFor(key), nil, nil)
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
	resp, err := s.do(ctx, http.MethodDelete, s.urlFor(key), nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode/100 == 2 {
		return nil
	}
	return fmt.Errorf("webdav delete %s: %s", key, resp.Status)
}

// List returns objects whose key begins with prefix, via a Depth:1 PROPFIND.
func (s *WebDAVStore) List(ctx context.Context, prefix string) ([]storemod.Object, error) {
	body := strings.NewReader(propfindBody)
	resp, err := s.do(ctx, "PROPFIND", s.urlFor(prefix), body, map[string]string{"Depth": "1", "Content-Type": "application/xml"})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusMultiStatus && resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("webdav propfind: %s", resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	basePath := "/"
	if u, err := url.Parse(s.Opts.BaseURL); err == nil {
		basePath = u.Path
	}
	return parseMultistatus(data, basePath, prefix)
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
			ContentLength string `xml:"getcontentlength"`
			ContentType   string `xml:"getcontenttype"`
			LastModified  string `xml:"getlastmodified"`
			Collection    *struct{} `xml:"resourcetype>collection"`
		} `xml:"prop"`
	} `xml:"propstat"`
}

type multistatus struct {
	Responses []msResponse `xml:"response"`
}

func parseMultistatus(data []byte, basePath, prefix string) ([]storemod.Object, error) {
	var ms multistatus
	if err := xml.NewDecoder(bytes.NewReader(data)).Decode(&ms); err != nil {
		return nil, err
	}
	var out []storemod.Object
	for _, r := range ms.Responses {
		if len(r.Propstat) == 0 {
			continue
		}
		p := r.Propstat[0].Prop
		if p.Collection != nil {
			continue // skip directories, including the queried collection itself
		}
		key := hrefToKey(basePath, r.Href)
		if key == "" || !strings.HasPrefix(key, prefix) {
			continue
		}
		obj := storemod.Object{Key: key, ContentType: p.ContentType}
		if p.ContentLength != "" {
			obj.Size, _ = strconv.ParseInt(p.ContentLength, 10, 64)
		}
		if p.LastModified != "" {
			if t, err := http.ParseTime(p.LastModified); err == nil {
				obj.ModTime = t
			}
		}
		out = append(out, obj)
	}
	return out, nil
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
