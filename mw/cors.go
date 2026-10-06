package mw

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bluebeard63/bosun"
)

// --- CORS ---

// CORSOptions configures the CORS middleware. The zero value allows no
// cross-origin requests; list the origins you trust in AllowedOrigins.
type CORSOptions struct {
	// AllowedOrigins lists origins allowed to make cross-origin requests,
	// e.g. "https://app.example.com". "*" allows any origin. A single
	// wildcard subdomain such as "https://*.example.com" matches any
	// subdomain (but not example.com itself). Matching is case-insensitive.
	AllowedOrigins []string
	// AllowOriginFunc, if set, is consulted for origins not matched by
	// AllowedOrigins.
	AllowOriginFunc func(origin string) bool `json:"-"`
	// AllowedMethods for preflighted requests. Default: GET, HEAD, POST,
	// PUT, PATCH, DELETE.
	AllowedMethods []string
	// AllowedHeaders the client may send. Default: Accept, Authorization,
	// Content-Type, X-Correlation-ID. "*" allows any requested header.
	AllowedHeaders []string
	// ExposedHeaders lists response headers browser scripts may read beyond
	// the CORS-safelisted ones (e.g. "X-Correlation-ID").
	ExposedHeaders []string
	// AllowCredentials permits cookies / HTTP auth on cross-origin requests.
	// With credentials the response echoes the request origin, never "*".
	AllowCredentials bool
	// MaxAge is how long browsers may cache a preflight result. Zero omits
	// the header (browser default, typically 5s); it is sent in whole seconds.
	MaxAge time.Duration
}

var _ = bosun.DefaultDynamic[CORSOptions](func() *CORSOptions { return &CORSOptions{} })

// CORS implements cross-origin resource sharing: it answers preflight
// (OPTIONS + Access-Control-Request-Method) requests itself with 204 and adds
// the Access-Control-* headers to actual cross-origin requests from allowed
// origins. Requests without an Origin header pass through untouched;
// disallowed origins get no CORS headers, so the browser blocks them.
//
// Register it app-wide: ServeMux routes registered for GET/POST/... never
// receive OPTIONS, so preflights only reach CORS when it wraps the whole app.
//
// Static configuration per use:
//
//	app := bosun.New(bosun.WithMiddleware(
//	    bosun.Use[mw.CORS](mw.CORSOptions{
//	        AllowedOrigins:   []string{"https://app.example.com"},
//	        AllowCredentials: true,
//	    }),
//	))
//
// Or with no arguments, CORS reads the injected *bosun.Dynamic[CORSOptions]
// per request, so the policy can be hot-reloaded via the config module.
type CORS struct {
	opts *bosun.Dynamic[CORSOptions] // injected, hot-reloadable

	cache atomic.Pointer[corsCacheEntry]
}

type corsCacheEntry struct {
	src *CORSOptions
	pol *corsPolicy
}

// Configure returns a CORS handler with a fixed policy, used by
// bosun.Use[mw.CORS](opts).
func (m *CORS) Configure(opts CORSOptions) bosun.MiddlewareHandler {
	pol := compileCORS(&opts)
	return bosun.MiddlewareFunc(func(next http.Handler) http.Handler {
		return pol.handle(next)
	})
}

func (m *CORS) Handle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.policy().serve(w, r, next)
	})
}

// policy compiles the current dynamic options, reusing the last compilation
// while the underlying *CORSOptions pointer is unchanged.
func (m *CORS) policy() *corsPolicy {
	var src *CORSOptions
	if m.opts != nil {
		src = m.opts.Get()
	}
	if src == nil {
		src = &CORSOptions{}
	}
	if e := m.cache.Load(); e != nil && e.src == src {
		return e.pol
	}
	pol := compileCORS(src)
	m.cache.Store(&corsCacheEntry{src: src, pol: pol})
	return pol
}

var _ = bosun.Middleware[CORS]()

// --- compiled policy ---

type corsPolicy struct {
	anyOrigin     bool
	origins       map[string]bool
	wildcards     [][2]string // {prefix, suffix}
	originFunc    func(string) bool
	methods       []string
	methodsHeader string
	anyHeader     bool
	headers       map[string]bool
	exposed       string
	credentials   bool
	maxAge        string
}

var (
	defaultCORSMethods = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"}
	defaultCORSHeaders = []string{"Accept", "Authorization", "Content-Type", bosun.HeaderCorrelationID}
)

func compileCORS(o *CORSOptions) *corsPolicy {
	p := &corsPolicy{
		origins:     map[string]bool{},
		headers:     map[string]bool{},
		originFunc:  o.AllowOriginFunc,
		credentials: o.AllowCredentials,
	}
	for _, origin := range o.AllowedOrigins {
		origin = strings.ToLower(strings.TrimSpace(origin))
		switch {
		case origin == "*":
			p.anyOrigin = true
		case strings.Count(origin, "*") == 1:
			pre, suf, _ := strings.Cut(origin, "*")
			p.wildcards = append(p.wildcards, [2]string{pre, suf})
		case origin != "":
			p.origins[origin] = true
		}
	}

	methods := o.AllowedMethods
	if len(methods) == 0 {
		methods = defaultCORSMethods
	}
	for _, m := range methods {
		p.methods = append(p.methods, strings.ToUpper(strings.TrimSpace(m)))
	}
	p.methodsHeader = strings.Join(p.methods, ", ")

	headers := o.AllowedHeaders
	if len(headers) == 0 {
		headers = defaultCORSHeaders
	}
	for _, h := range headers {
		h = strings.TrimSpace(h)
		if h == "*" {
			p.anyHeader = true
			continue
		}
		p.headers[http.CanonicalHeaderKey(h)] = true
	}

	p.exposed = strings.Join(o.ExposedHeaders, ", ")
	if o.MaxAge > 0 {
		p.maxAge = strconv.Itoa(int(o.MaxAge / time.Second))
	}
	return p
}

func (p *corsPolicy) handle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { p.serve(w, r, next) })
}

func (p *corsPolicy) serve(w http.ResponseWriter, r *http.Request, next http.Handler) {
	origin := r.Header.Get("Origin")
	preflight := r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
	h := w.Header()

	if preflight {
		h.Add("Vary", "Origin")
		h.Add("Vary", "Access-Control-Request-Method")
		h.Add("Vary", "Access-Control-Request-Headers")
		if origin != "" && p.allowOrigin(origin) && p.allowPreflight(r) {
			p.setOrigin(h, origin)
			h.Set("Access-Control-Allow-Methods", p.methodsHeader)
			if req := r.Header.Get("Access-Control-Request-Headers"); req != "" {
				h.Set("Access-Control-Allow-Headers", req)
			}
			if p.maxAge != "" {
				h.Set("Access-Control-Max-Age", p.maxAge)
			}
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	h.Add("Vary", "Origin")
	if origin != "" && p.allowOrigin(origin) {
		p.setOrigin(h, origin)
		if p.exposed != "" {
			h.Set("Access-Control-Expose-Headers", p.exposed)
		}
	}
	next.ServeHTTP(w, r)
}

func (p *corsPolicy) setOrigin(h http.Header, origin string) {
	if p.anyOrigin && !p.credentials {
		h.Set("Access-Control-Allow-Origin", "*")
	} else {
		h.Set("Access-Control-Allow-Origin", origin)
	}
	if p.credentials {
		h.Set("Access-Control-Allow-Credentials", "true")
	}
}

func (p *corsPolicy) allowOrigin(origin string) bool {
	if p.anyOrigin {
		return true
	}
	o := strings.ToLower(origin)
	if p.origins[o] {
		return true
	}
	for _, w := range p.wildcards {
		if len(o) > len(w[0])+len(w[1]) && strings.HasPrefix(o, w[0]) && strings.HasSuffix(o, w[1]) {
			return true
		}
	}
	return p.originFunc != nil && p.originFunc(origin)
}

func (p *corsPolicy) allowPreflight(r *http.Request) bool {
	if !slices.Contains(p.methods, strings.ToUpper(r.Header.Get("Access-Control-Request-Method"))) {
		return false
	}
	if p.anyHeader {
		return true
	}
	for _, name := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
		name = strings.TrimSpace(name)
		if name != "" && !p.headers[http.CanonicalHeaderKey(name)] {
			return false
		}
	}
	return true
}
