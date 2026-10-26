package mw

import (
	"bufio"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/bluebeard63/bosun"
)

// --- SecureHeaders ---

// SecureHeadersOptions lists the security headers to send. Each string
// field is sent verbatim when non-empty and omitted when empty, so start
// from DefaultSecureHeaders() and adjust rather than from the zero value.
// X-Content-Type-Options: nosniff is always sent.
type SecureHeadersOptions struct {
	// HSTSMaxAge is the Strict-Transport-Security max-age. Zero omits the
	// header. Browsers ignore HSTS on plain-HTTP responses, so it is safe
	// to send behind a TLS-terminating proxy and in local development.
	HSTSMaxAge time.Duration
	// HSTSIncludeSubdomains adds includeSubDomains to HSTS. Enable it only
	// once every subdomain serves HTTPS.
	HSTSIncludeSubdomains bool
	// HSTSPreload adds the preload directive (for the browser preload list).
	HSTSPreload bool
	// FrameOptions is X-Frame-Options: "DENY" or "SAMEORIGIN".
	FrameOptions string
	// ReferrerPolicy is Referrer-Policy, e.g. "strict-origin-when-cross-origin".
	ReferrerPolicy string
	// ContentSecurityPolicy is Content-Security-Policy. For a JSON-only API,
	// "default-src 'none'; frame-ancestors 'none'" is a good choice; pages
	// that load scripts or styles need a policy that allows them.
	ContentSecurityPolicy string
	// CrossOriginOpenerPolicy is Cross-Origin-Opener-Policy, e.g. "same-origin".
	CrossOriginOpenerPolicy string
	// PermissionsPolicy is Permissions-Policy, e.g. "camera=(), microphone=()".
	PermissionsPolicy string
}

// DefaultSecureHeaders returns the defaults used by bosun.Use[mw.SecureHeaders]()
// with no arguments: HSTS for one year (no subdomains, no preload),
// X-Frame-Options: DENY, Referrer-Policy: strict-origin-when-cross-origin
// and Cross-Origin-Opener-Policy: same-origin. No Content-Security-Policy
// or Permissions-Policy is set, since the right values depend on the app.
func DefaultSecureHeaders() SecureHeadersOptions {
	return SecureHeadersOptions{
		HSTSMaxAge:              365 * 24 * time.Hour,
		FrameOptions:            "DENY",
		ReferrerPolicy:          "strict-origin-when-cross-origin",
		CrossOriginOpenerPolicy: "same-origin",
	}
}

var _ = bosun.DefaultDynamic[SecureHeadersOptions](func() *SecureHeadersOptions {
	o := DefaultSecureHeaders()
	return &o
})

// SecureHeaders adds common security response headers. They are applied
// when the response is committed (first WriteHeader, Write or Flush), and
// only where the handler has not set the header itself, so a handler can
// override any of them per response. Applying them late also means they
// survive Recover, which clears handler headers before writing its 500.
//
// Register it app-wide:
//
//	app := bosun.New(bosun.WithMiddleware(
//	    bosun.Use[mw.SecureHeaders](),     // defaults
//	    bosun.Use[mw.Recover](),
//	))
//
//	opts := mw.DefaultSecureHeaders()
//	opts.ContentSecurityPolicy = "default-src 'none'; frame-ancestors 'none'"
//	bosun.Use[mw.SecureHeaders](opts)   // fixed policy
//
// With no arguments SecureHeaders reads the injected
// *bosun.Dynamic[SecureHeadersOptions] (seeded with DefaultSecureHeaders)
// per request.
type SecureHeaders struct {
	opts *bosun.Dynamic[SecureHeadersOptions] // injected, hot-reloadable

	cache atomic.Pointer[secureHeadersCacheEntry]
}

type secureHeadersCacheEntry struct {
	src     *SecureHeadersOptions
	headers [][2]string
}

// Configure returns a SecureHeaders handler with a fixed header set, used
// by bosun.Use[mw.SecureHeaders](opts).
func (m *SecureHeaders) Configure(opts SecureHeadersOptions) bosun.MiddlewareHandler {
	headers := compileSecureHeaders(&opts)
	return bosun.MiddlewareFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serveSecureHeaders(headers, w, r, next)
		})
	})
}

func (m *SecureHeaders) Handle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveSecureHeaders(m.headers(), w, r, next)
	})
}

func serveSecureHeaders(headers [][2]string, w http.ResponseWriter, r *http.Request, next http.Handler) {
	sw := &secureHeadersWriter{ResponseWriter: w, headers: headers}
	next.ServeHTTP(sw, r)
	sw.apply() // handler wrote nothing: net/http sends the headers after we return
}

func (m *SecureHeaders) headers() [][2]string {
	var src *SecureHeadersOptions
	if m.opts != nil {
		src = m.opts.Get()
	}
	if src == nil {
		d := DefaultSecureHeaders()
		src = &d
	}
	if e := m.cache.Load(); e != nil && e.src == src {
		return e.headers
	}
	h := compileSecureHeaders(src)
	m.cache.Store(&secureHeadersCacheEntry{src: src, headers: h})
	return h
}

var _ = bosun.Middleware[SecureHeaders]()

func compileSecureHeaders(o *SecureHeadersOptions) [][2]string {
	out := [][2]string{{"X-Content-Type-Options", "nosniff"}}
	if o.HSTSMaxAge > 0 {
		v := "max-age=" + strconv.FormatInt(int64(o.HSTSMaxAge/time.Second), 10)
		if o.HSTSIncludeSubdomains {
			v += "; includeSubDomains"
		}
		if o.HSTSPreload {
			v += "; preload"
		}
		out = append(out, [2]string{"Strict-Transport-Security", v})
	}
	for _, kv := range [][2]string{
		{"X-Frame-Options", o.FrameOptions},
		{"Referrer-Policy", o.ReferrerPolicy},
		{"Content-Security-Policy", o.ContentSecurityPolicy},
		{"Cross-Origin-Opener-Policy", o.CrossOriginOpenerPolicy},
		{"Permissions-Policy", o.PermissionsPolicy},
	} {
		if kv[1] != "" {
			out = append(out, kv)
		}
	}
	return out
}

// secureHeadersWriter fills in the security headers the handler left unset
// when the response is committed. It forwards Flush, Hijack and Unwrap.
type secureHeadersWriter struct {
	http.ResponseWriter
	headers [][2]string
	applied bool
}

func (w *secureHeadersWriter) apply() {
	if w.applied {
		return
	}
	w.applied = true
	h := w.Header()
	for _, kv := range w.headers {
		if _, set := h[kv[0]]; !set {
			h.Set(kv[0], kv[1])
		}
	}
}

func (w *secureHeadersWriter) WriteHeader(code int) {
	if code >= 200 || code == http.StatusSwitchingProtocols {
		w.apply()
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *secureHeadersWriter) Write(b []byte) (int, error) {
	w.apply()
	return w.ResponseWriter.Write(b)
}

func (w *secureHeadersWriter) Flush() {
	w.apply()
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *secureHeadersWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	w.applied = true
	return h.Hijack()
}

func (w *secureHeadersWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
