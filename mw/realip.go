package mw

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"

	"github.com/bluebeard63/bosun"
)

// --- RealIP ---

// RealIPOptions configures RealIP. The zero value trusts no proxy, so
// forwarding headers are ignored and the client IP is always the TCP peer.
type RealIPOptions struct {
	// TrustedProxies lists the proxies (load balancers, ingress, CDN edge)
	// allowed to report the client address, as CIDRs ("10.0.0.0/8",
	// "fd00::/8") or single IPs ("127.0.0.1"). Forwarding headers are only
	// read when the immediate peer is in this list.
	TrustedProxies []string
}

var _ = bosun.DefaultDynamic[RealIPOptions](func() *RealIPOptions { return &RealIPOptions{} })

// RealIP resolves the originating client IP of requests that arrive through
// trusted reverse proxies, and stores it on the request context where
// ClientIP (and mw.RateLimit, mw.Logging) read it. r.RemoteAddr is left
// untouched, so it always names the actual TCP peer.
//
// When the immediate peer (r.RemoteAddr) is a trusted proxy, RealIP reads,
// in order of preference, X-Forwarded-For, Forwarded (RFC 7239, the for=
// parameters) and X-Real-IP. The X-Forwarded-For and Forwarded chains are
// walked right to left, skipping addresses that are themselves trusted
// proxies; the first untrusted address is the client. Entries to the left
// of it were supplied by the client and are never believed. When the peer
// is not trusted, the headers are ignored and the client is the peer.
//
// Register it app-wide and outermost (before Logging and RateLimit), with
// the CIDRs of your proxies:
//
//	app := bosun.New(bosun.WithMiddleware(
//	    bosun.Use[mw.RealIP](mw.RealIPOptions{TrustedProxies: []string{"10.0.0.0/8"}}),
//	    bosun.Use[mw.Correlation](),
//	    bosun.Use[mw.Logging](),
//	))
//
// Configure panics on an invalid CIDR so the mistake fails at startup. With
// no arguments RealIP reads the injected *bosun.Dynamic[RealIPOptions] per
// request; an invalid hot-reloaded list trusts no proxy.
type RealIP struct {
	opts *bosun.Dynamic[RealIPOptions] // injected, hot-reloadable

	cache atomic.Pointer[realIPCacheEntry]
}

type realIPCacheEntry struct {
	src     *RealIPOptions
	trusted *trustedProxies
}

// Configure returns a RealIP handler with a fixed proxy list, used by
// bosun.Use[mw.RealIP](opts).
func (m *RealIP) Configure(opts RealIPOptions) bosun.MiddlewareHandler {
	tp, err := parseTrustedProxies(opts.TrustedProxies)
	if err != nil {
		panic(err)
	}
	return bosun.MiddlewareFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serveRealIP(tp, w, r, next)
		})
	})
}

func (m *RealIP) Handle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveRealIP(m.trusted(), w, r, next)
	})
}

func (m *RealIP) trusted() *trustedProxies {
	var src *RealIPOptions
	if m.opts != nil {
		src = m.opts.Get()
	}
	if src == nil {
		src = &RealIPOptions{}
	}
	if e := m.cache.Load(); e != nil && e.src == src {
		return e.trusted
	}
	tp, err := parseTrustedProxies(src.TrustedProxies)
	if err != nil {
		tp = &trustedProxies{} // fail closed: trust nobody
	}
	m.cache.Store(&realIPCacheEntry{src: src, trusted: tp})
	return tp
}

var _ = bosun.Middleware[RealIP]()

// clientIPValue is the context slot RealIP fills.
type clientIPValue struct{ ip string }

// ClientIP returns the originating client IP of r: the address resolved by
// mw.RealIP when it ran upstream, otherwise the host part of r.RemoteAddr.
func ClientIP(r *http.Request) string {
	if v := bosun.Value[clientIPValue](r.Context()); v != nil {
		return v.ip
	}
	return remoteHost(r.RemoteAddr)
}

// resolvedClientIP returns the IP RealIP stored on r, or "" if it didn't run.
func resolvedClientIP(r *http.Request) string {
	if v := bosun.Value[clientIPValue](r.Context()); v != nil {
		return v.ip
	}
	return ""
}

func remoteHost(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

func serveRealIP(tp *trustedProxies, w http.ResponseWriter, r *http.Request, next http.Handler) {
	ip := tp.clientIP(r)
	next.ServeHTTP(w, r.WithContext(bosun.WithValue(r.Context(), &clientIPValue{ip: ip})))
}

// --- trusted proxy list ---

type trustedProxies struct {
	prefixes []netip.Prefix
}

func parseTrustedProxies(list []string) (*trustedProxies, error) {
	tp := &trustedProxies{}
	for _, s := range list {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if strings.Contains(s, "/") {
			p, err := netip.ParsePrefix(s)
			if err != nil {
				return nil, fmt.Errorf("mw.RealIP: invalid trusted proxy %q: %w", s, err)
			}
			tp.prefixes = append(tp.prefixes, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, fmt.Errorf("mw.RealIP: invalid trusted proxy %q: %w", s, err)
		}
		a = a.Unmap()
		tp.prefixes = append(tp.prefixes, netip.PrefixFrom(a, a.BitLen()))
	}
	return tp, nil
}

func (tp *trustedProxies) contains(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range tp.prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// clientIP resolves the client address of r against the trusted list.
func (tp *trustedProxies) clientIP(r *http.Request) string {
	peerStr := remoteHost(r.RemoteAddr)
	peer, err := netip.ParseAddr(peerStr)
	if err != nil || len(tp.prefixes) == 0 || !tp.contains(peer) {
		return peerStr
	}
	if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
		return tp.walk(peer, splitList(xff, parseXFFEntry))
	}
	if fwd := r.Header.Values("Forwarded"); len(fwd) > 0 {
		return tp.walk(peer, splitList(fwd, parseForwardedEntry))
	}
	if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
		if a, ok := parseHostAddr(xr); ok {
			return a.Unmap().String()
		}
	}
	return peer.Unmap().String()
}

// walk scans hops right to left (nearest proxy first), returning the first
// address that is not a trusted proxy. An unparseable hop stops the walk:
// the trusted proxy that reported it is the furthest address we can vouch
// for. If every hop is trusted, the leftmost one is the client.
func (tp *trustedProxies) walk(peer netip.Addr, hops []string) string {
	last := peer.Unmap()
	for i := len(hops) - 1; i >= 0; i-- {
		a, ok := parseHostAddr(hops[i])
		if !ok {
			return last.String()
		}
		a = a.Unmap()
		if !tp.contains(a) {
			return a.String()
		}
		last = a
	}
	return last.String()
}

// splitList flattens comma-separated header values, mapping each element.
func splitList(values []string, each func(string) string) []string {
	var out []string
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			out = append(out, each(strings.TrimSpace(part)))
		}
	}
	return out
}

func parseXFFEntry(s string) string { return s }

// parseForwardedEntry extracts the for= value from one Forwarded element
// such as `for="[2001:db8::1]:4711";proto=https`.
func parseForwardedEntry(s string) string {
	for _, pair := range strings.Split(s, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), "for") {
			return strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	return ""
}

// parseHostAddr parses an IP optionally carrying a port or IPv6 brackets:
// "1.2.3.4", "1.2.3.4:80", "2001:db8::1", "[2001:db8::1]:80".
func parseHostAddr(s string) (netip.Addr, bool) {
	if a, err := netip.ParseAddr(s); err == nil {
		return a, true
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr(), true
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		if a, err := netip.ParseAddr(s[1 : len(s)-1]); err == nil {
			return a, true
		}
	}
	return netip.Addr{}, false
}
