package mw

import (
	"bufio"
	"compress/gzip"
	"compress/zlib"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/bluebeard63/bosun"
)

// --- Compress ---

// CompressOptions configures Compress. Zero fields take the defaults, so
// CompressOptions{} is a sensible configuration.
type CompressOptions struct {
	// Level is the compression level, 1 (fastest) to 9 (smallest). Zero
	// means the library default (6); -2 (gzip.HuffmanOnly) is also accepted.
	Level int
	// MinSize is the smallest body, in bytes, worth compressing. Smaller
	// responses are sent as-is. Zero means 1024.
	MinSize int
	// ContentTypes lists the media types to compress. An entry may be exact
	// ("application/json"), a type wildcard ("text/*") or a suffix wildcard
	// ("*+json"). Empty means DefaultCompressTypes.
	ContentTypes []string
}

// DefaultCompressTypes are the media types Compress handles when
// CompressOptions.ContentTypes is empty: text formats, JSON, XML,
// JavaScript, SVG and WebAssembly. Already-compressed formats (images,
// video, archives, fonts) are left alone.
var DefaultCompressTypes = []string{
	"text/*",
	"application/json",
	"application/javascript",
	"application/x-javascript",
	"application/xml",
	"application/x-ndjson",
	"application/wasm",
	"image/svg+xml",
	"*+json",
	"*+xml",
}

const defaultCompressMinSize = 1024

var _ = bosun.DefaultDynamic[CompressOptions](func() *CompressOptions { return &CompressOptions{} })

// Compress compresses responses with gzip, or deflate when the client
// prefers it, negotiated from Accept-Encoding (q-values honoured). It uses
// only the standard library; Brotli and zstd are not supported.
//
// A response is compressed only when all of these hold: the client accepts
// gzip or deflate; the status is not 1xx, 204, 206 or 304; the handler set
// no Content-Encoding, Content-Range or Cache-Control: no-transform; the
// Content-Type (sniffed from the body if the handler set none) is in
// ContentTypes; and the body reaches MinSize bytes. Compress buffers up to
// MinSize bytes to decide, unless the handler declares a Content-Length.
//
// When it compresses, Compress sets Content-Encoding, drops Content-Length
// and Accept-Ranges, and weakens a strong ETag (the bytes differ from the
// identity representation). Every response gets Vary: Accept-Encoding.
//
// Streaming works: Flush flushes the compressor and then the connection, so
// SSE and chunked responses arrive incrementally. Hijack (WebSockets) and
// http.ResponseController (via Unwrap) are forwarded.
//
// Register it app-wide, outside Recover so a panic still yields a plain,
// uncompressed 500:
//
//	app := bosun.New(bosun.WithMiddleware(
//	    bosun.Use[mw.Compress](mw.CompressOptions{}),
//	    bosun.Use[mw.Recover](),
//	))
//
// With no arguments Compress reads the injected
// *bosun.Dynamic[CompressOptions] per request.
type Compress struct {
	opts *bosun.Dynamic[CompressOptions] // injected, hot-reloadable

	cache atomic.Pointer[compressCacheEntry]
}

type compressCacheEntry struct {
	src *CompressOptions
	cfg *compressConfig
}

// Configure returns a Compress handler with fixed options, used by
// bosun.Use[mw.Compress](opts). It panics on an invalid Level.
func (m *Compress) Configure(opts CompressOptions) bosun.MiddlewareHandler {
	cfg, err := compileCompress(&opts)
	if err != nil {
		panic(err)
	}
	return bosun.MiddlewareFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cfg.serve(w, r, next)
		})
	})
}

func (m *Compress) Handle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.config().serve(w, r, next)
	})
}

func (m *Compress) config() *compressConfig {
	var src *CompressOptions
	if m.opts != nil {
		src = m.opts.Get()
	}
	if src == nil {
		src = &CompressOptions{}
	}
	if e := m.cache.Load(); e != nil && e.src == src {
		return e.cfg
	}
	cfg, err := compileCompress(src)
	if err != nil {
		cfg, _ = compileCompress(&CompressOptions{})
	}
	m.cache.Store(&compressCacheEntry{src: src, cfg: cfg})
	return cfg
}

var _ = bosun.Middleware[Compress]()

// --- compiled config ---

type compressConfig struct {
	level    int
	minSize  int
	exact    map[string]bool
	prefixes []string // "text/"
	suffixes []string // "+json"
}

func compileCompress(o *CompressOptions) (*compressConfig, error) {
	c := &compressConfig{level: o.Level, minSize: o.MinSize, exact: map[string]bool{}}
	if c.level == 0 {
		c.level = gzip.DefaultCompression
	}
	if c.level != gzip.DefaultCompression && c.level != gzip.HuffmanOnly &&
		(c.level < gzip.BestSpeed || c.level > gzip.BestCompression) {
		return nil, fmt.Errorf("mw.Compress: invalid compression level %d", o.Level)
	}
	if c.minSize <= 0 {
		c.minSize = defaultCompressMinSize
	}
	types := o.ContentTypes
	if len(types) == 0 {
		types = DefaultCompressTypes
	}
	for _, t := range types {
		t = strings.ToLower(strings.TrimSpace(t))
		switch {
		case t == "":
		case strings.HasPrefix(t, "*"):
			c.suffixes = append(c.suffixes, t[1:])
		case strings.HasSuffix(t, "/*"):
			c.prefixes = append(c.prefixes, t[:len(t)-1])
		default:
			c.exact[t] = true
		}
	}
	return c, nil
}

func (c *compressConfig) compressible(contentType string) bool {
	mt, _, _ := strings.Cut(contentType, ";")
	mt = strings.ToLower(strings.TrimSpace(mt))
	if mt == "" {
		return false
	}
	if c.exact[mt] {
		return true
	}
	for _, p := range c.prefixes {
		if strings.HasPrefix(mt, p) {
			return true
		}
	}
	for _, s := range c.suffixes {
		if strings.HasSuffix(mt, s) {
			return true
		}
	}
	return false
}

func (c *compressConfig) serve(w http.ResponseWriter, r *http.Request, next http.Handler) {
	cw := &compressWriter{ResponseWriter: w, cfg: c, enc: negotiateEncoding(r.Header.Values("Accept-Encoding"))}
	next.ServeHTTP(cw, r)
	cw.finish()
}

// negotiateEncoding picks "gzip", "deflate" or "" from Accept-Encoding,
// honouring q-values; gzip wins ties.
func negotiateEncoding(values []string) string {
	gz, df, star := -1.0, -1.0, -1.0
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			name, params, _ := strings.Cut(part, ";")
			q := 1.0
			if k, val, ok := strings.Cut(strings.TrimSpace(params), "="); ok && strings.EqualFold(strings.TrimSpace(k), "q") {
				f, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
				if err != nil {
					continue
				}
				q = f
			}
			switch strings.ToLower(strings.TrimSpace(name)) {
			case "gzip", "x-gzip":
				gz = q
			case "deflate":
				df = q
			case "*":
				star = q
			}
		}
	}
	if gz < 0 {
		gz = star
	}
	if df < 0 {
		df = star
	}
	switch {
	case gz > 0 && gz >= df:
		return "gzip"
	case df > 0:
		return "deflate"
	}
	return ""
}

// --- writer ---

type compressState int

const (
	compressUndecided compressState = iota
	compressPassthrough
	compressActive
)

// compressWriter decides per response whether to compress, buffering up to
// MinSize bytes when the decision depends on the body.
type compressWriter struct {
	http.ResponseWriter
	cfg *compressConfig
	enc string // negotiated encoding, "" when the client accepts none

	state       compressState
	wroteHeader bool
	status      int
	buf         []byte
	zw          compressor
	hijacked    bool
}

func (w *compressWriter) WriteHeader(code int) {
	if w.wroteHeader || w.hijacked {
		if w.state == compressPassthrough {
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
	h := w.Header()
	switch {
	case w.enc == "", !compressibleStatus(code), alreadyEncoded(h):
		w.passthrough()
	case h.Get("Content-Type") != "" && !w.cfg.compressible(h.Get("Content-Type")):
		w.passthrough()
	case h.Get("Content-Length") != "":
		n, err := strconv.Atoi(h.Get("Content-Length"))
		switch {
		case err != nil || n < w.cfg.minSize:
			w.passthrough()
		case h.Get("Content-Type") != "":
			w.start()
		}
	}
}

func (w *compressWriter) Write(b []byte) (int, error) {
	if w.hijacked {
		return 0, http.ErrHijacked
	}
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	switch w.state {
	case compressPassthrough:
		return w.ResponseWriter.Write(b)
	case compressActive:
		return w.zw.Write(b)
	}
	w.buf = append(w.buf, b...)
	if len(w.buf) >= w.cfg.minSize {
		if err := w.decide(); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}

// decide commits an undecided response based on its (possibly sniffed)
// Content-Type and writes any buffered bytes.
func (w *compressWriter) decide() error {
	h := w.Header()
	if _, ok := h["Content-Type"]; !ok && len(w.buf) > 0 {
		h.Set("Content-Type", http.DetectContentType(w.buf))
	}
	if w.cfg.compressible(h.Get("Content-Type")) {
		w.start()
	} else {
		w.passthrough()
	}
	return w.drain()
}

func (w *compressWriter) drain() error {
	if len(w.buf) == 0 {
		return nil
	}
	buf := w.buf
	w.buf = nil
	var err error
	if w.state == compressActive {
		_, err = w.zw.Write(buf)
	} else {
		_, err = w.ResponseWriter.Write(buf)
	}
	return err
}

func (w *compressWriter) passthrough() {
	w.state = compressPassthrough
	ensureVary(w.Header(), "Accept-Encoding")
	w.ResponseWriter.WriteHeader(w.status)
}

func (w *compressWriter) start() {
	w.state = compressActive
	h := w.Header()
	ensureVary(h, "Accept-Encoding")
	h.Set("Content-Encoding", w.enc)
	h.Del("Content-Length")
	h.Del("Accept-Ranges")
	if et := h.Get("ETag"); et != "" && !strings.HasPrefix(et, "W/") {
		h.Set("ETag", "W/"+et)
	}
	w.ResponseWriter.WriteHeader(w.status)
	w.zw = getCompressor(w.enc, w.cfg.level, w.ResponseWriter)
}

// finish runs after the handler returns: it sends a small buffered body
// as-is, or closes the compressor so the trailer is written.
func (w *compressWriter) finish() {
	if w.hijacked {
		return
	}
	switch w.state {
	case compressUndecided:
		if !w.wroteHeader {
			ensureVary(w.Header(), "Accept-Encoding") // nothing written; net/http sends 200
			return
		}
		w.passthrough()
		_ = w.drain()
	case compressActive:
		_ = w.zw.Close()
		putCompressor(w.enc, w.cfg.level, w.zw)
		w.zw = nil
	}
}

// Flush commits the response (compressing it if its type qualifies,
// whatever its size so far), flushes the compressor and then the client
// connection.
func (w *compressWriter) Flush() {
	if w.hijacked {
		return
	}
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.state == compressUndecided {
		_ = w.decide()
	}
	if w.state == compressActive {
		_ = w.zw.Flush()
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *compressWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	conn, rw, err := h.Hijack()
	if err == nil {
		w.hijacked = true
	}
	return conn, rw, err
}

func (w *compressWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func compressibleStatus(code int) bool {
	switch {
	case code < 200, code == http.StatusNoContent, code == http.StatusPartialContent,
		code == http.StatusNotModified:
		return false
	}
	return true
}

func alreadyEncoded(h http.Header) bool {
	if ce := h.Get("Content-Encoding"); ce != "" && !strings.EqualFold(ce, "identity") {
		return true
	}
	if h.Get("Content-Range") != "" {
		return true
	}
	for _, v := range h.Values("Cache-Control") {
		if strings.Contains(strings.ToLower(v), "no-transform") {
			return true
		}
	}
	return false
}

// ensureVary adds token to the Vary header unless it (or "*") is present.
func ensureVary(h http.Header, token string) {
	for _, v := range h.Values("Vary") {
		for _, t := range strings.Split(v, ",") {
			t = strings.TrimSpace(t)
			if t == "*" || strings.EqualFold(t, token) {
				return
			}
		}
	}
	h.Add("Vary", token)
}

// --- pooled compressors ---

type compressor interface {
	io.Writer
	Flush() error
	Close() error
	Reset(io.Writer)
}

type compressorKey struct {
	enc   string
	level int
}

var compressorPools sync.Map // compressorKey -> *sync.Pool

func compressorPool(enc string, level int) *sync.Pool {
	key := compressorKey{enc, level}
	if p, ok := compressorPools.Load(key); ok {
		return p.(*sync.Pool)
	}
	p, _ := compressorPools.LoadOrStore(key, &sync.Pool{New: func() any {
		var c compressor
		if enc == "deflate" {
			c, _ = zlib.NewWriterLevel(io.Discard, level)
		} else {
			c, _ = gzip.NewWriterLevel(io.Discard, level)
		}
		return c
	}})
	return p.(*sync.Pool)
}

func getCompressor(enc string, level int, w io.Writer) compressor {
	c := compressorPool(enc, level).Get().(compressor)
	c.Reset(w)
	return c
}

func putCompressor(enc string, level int, c compressor) {
	c.Reset(io.Discard)
	compressorPool(enc, level).Put(c)
}
