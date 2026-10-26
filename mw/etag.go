package mw

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/bluebeard63/bosun"
)

// --- ETag ---

// ETagOptions configures ETag. Zero fields take the defaults.
type ETagOptions struct {
	// MaxBodySize is the largest body, in bytes, that ETag buffers and
	// hashes. Larger responses stream through without an ETag. Zero means
	// 1 MiB.
	MaxBodySize int
	// Weak emits weak validators (W/"...") instead of strong ones.
	Weak bool
}

const defaultETagMaxBody = 1 << 20

var _ = bosun.DefaultDynamic[ETagOptions](func() *ETagOptions { return &ETagOptions{} })

// ETag adds an ETag to successful GET and HEAD responses and answers
// conditional requests: when If-None-Match matches (weak comparison, or
// "*"), the client gets 304 Not Modified with no body. The tag is a hash of
// the response body, so handlers need no changes; the handler still runs,
// ETag saves bandwidth, not work.
//
// To hash the body ETag buffers it, so it steps aside (streams the
// response untouched, without an ETag) whenever buffering would be wrong:
//   - the method is not GET or HEAD, or the status is not 200;
//   - the handler set its own ETag (e.g. http.ServeContent) — it is left
//     as-is;
//   - the body grows past MaxBodySize (default 1 MiB);
//   - the handler flushes (SSE, long polling) or hijacks the connection;
//   - a HEAD handler writes no body (there is nothing to hash).
//
// Place it inside Compress, so the hash covers the identity body and
// Compress weakens the tag on compressed responses:
//
//	app := bosun.New(bosun.WithMiddleware(
//	    bosun.Use[mw.Compress](mw.CompressOptions{}),
//	    bosun.Use[mw.ETag](mw.ETagOptions{}),
//	))
//
// With no arguments ETag reads the injected *bosun.Dynamic[ETagOptions].
type ETag struct {
	opts *bosun.Dynamic[ETagOptions] // injected, hot-reloadable
}

// Configure returns an ETag handler with fixed options, used by
// bosun.Use[mw.ETag](opts).
func (m *ETag) Configure(opts ETagOptions) bosun.MiddlewareHandler {
	return bosun.MiddlewareFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serveETag(&opts, w, r, next)
		})
	})
}

func (m *ETag) Handle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var o *ETagOptions
		if m.opts != nil {
			o = m.opts.Get()
		}
		if o == nil {
			o = &ETagOptions{}
		}
		serveETag(o, w, r, next)
	})
}

var _ = bosun.Middleware[ETag]()

func serveETag(o *ETagOptions, w http.ResponseWriter, r *http.Request, next http.Handler) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		next.ServeHTTP(w, r)
		return
	}
	limit := o.MaxBodySize
	if limit <= 0 {
		limit = defaultETagMaxBody
	}
	ew := &etagWriter{ResponseWriter: w, max: limit, weak: o.Weak, buffering: true}
	next.ServeHTTP(ew, r)
	ew.finish(r)
}

// etagWriter buffers a 200 body up to max bytes so it can be hashed, and
// falls back to streaming the moment buffering stops being safe.
type etagWriter struct {
	http.ResponseWriter
	max  int
	weak bool

	buffering   bool
	wroteHeader bool
	status      int
	buf         []byte
	hijacked    bool
}

func (w *etagWriter) WriteHeader(code int) {
	if w.wroteHeader || w.hijacked {
		if !w.buffering {
			w.ResponseWriter.WriteHeader(code) // let net/http report the superfluous call
		}
		return
	}
	if code < 200 && code != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(code) // informational; doesn't commit
		return
	}
	w.wroteHeader = true
	w.status = code
	if code != http.StatusOK || w.Header().Get("ETag") != "" {
		_ = w.stream() // nothing buffered yet; a write error resurfaces on Write
	}
}

func (w *etagWriter) Write(b []byte) (int, error) {
	if w.hijacked {
		return 0, http.ErrHijacked
	}
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if !w.buffering {
		return w.ResponseWriter.Write(b)
	}
	if len(w.buf)+len(b) > w.max {
		if err := w.stream(); err != nil {
			return 0, err
		}
		return w.ResponseWriter.Write(b)
	}
	w.buf = append(w.buf, b...)
	return len(b), nil
}

// stream abandons buffering: it sends the header and anything buffered.
func (w *etagWriter) stream() error {
	if !w.buffering {
		return nil
	}
	w.buffering = false
	w.ResponseWriter.WriteHeader(w.status)
	if len(w.buf) == 0 {
		return nil
	}
	_, err := w.ResponseWriter.Write(w.buf)
	w.buf = nil
	return err
}

func (w *etagWriter) finish(r *http.Request) {
	if w.hijacked || !w.buffering {
		return
	}
	if !w.wroteHeader {
		return // nothing written; net/http sends an empty 200
	}
	if r.Method == http.MethodHead && len(w.buf) == 0 {
		_ = w.stream()
		return
	}
	sum := sha256.Sum256(w.buf)
	tag := `"` + base64.RawURLEncoding.EncodeToString(sum[:16]) + `"`
	if w.weak {
		tag = "W/" + tag
	}
	h := w.Header()
	h.Set("ETag", tag)
	if inm := r.Header.Get("If-None-Match"); inm != "" && etagListMatches(inm, tag) {
		h.Del("Content-Type")
		h.Del("Content-Length")
		h.Del("Content-Encoding")
		w.ResponseWriter.WriteHeader(http.StatusNotModified)
		w.buffering = false
		return
	}
	if r.Method == http.MethodGet {
		h.Set("Content-Length", strconv.Itoa(len(w.buf)))
	}
	_ = w.stream()
}

func (w *etagWriter) Flush() {
	if w.hijacked {
		return
	}
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	_ = w.stream()
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *etagWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	conn, rw, err := h.Hijack()
	if err == nil {
		w.hijacked = true
		w.buffering = false
	}
	return conn, rw, err
}

func (w *etagWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// etagListMatches reports whether an If-None-Match value matches tag using
// the weak comparison (RFC 9110 13.1.2): "*" matches anything, and W/
// prefixes are ignored.
func etagListMatches(list, tag string) bool {
	opaque := strings.TrimPrefix(tag, "W/")
	for {
		list = strings.TrimLeft(list, " \t,")
		if list == "" {
			return false
		}
		if list[0] == '*' {
			return true
		}
		list = strings.TrimPrefix(list, "W/")
		if len(list) < 2 || list[0] != '"' {
			return false
		}
		end := strings.IndexByte(list[1:], '"')
		if end < 0 {
			return false
		}
		if list[:end+2] == opaque {
			return true
		}
		list = list[end+2:]
	}
}
