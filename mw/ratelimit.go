package mw

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/bluebeard63/bosun"
)

// --- RateLimit ---

// RateLimitOptions configures the built-in limiter. It is hot-reloadable:
// services read it through bosun.Dynamic, so updating the value (manually or
// via the config module) applies to live traffic immediately.
type RateLimitOptions struct {
	PerMinute int
}

var _ = bosun.DefaultDynamic[RateLimitOptions](func() *RateLimitOptions {
	return &RateLimitOptions{PerMinute: 60}
})

// RateLimit is a fixed-window per-IP limiter. It keys on ClientIP(r): the
// client address resolved by mw.RealIP when that runs upstream, otherwise
// the TCP peer. Behind a load balancer, register RealIP with the balancer's
// CIDRs, or every client shares the balancer's bucket. A rejected request
// gets 429 with Retry-After set to the whole seconds until its window resets.
//
// For multi-node deployments replace it with a shared-store implementation.
// Expired windows are swept from the map at most once per window length, so
// idle clients don't accumulate.
type RateLimit struct {
	opts *bosun.Dynamic[RateLimitOptions] // injected, hot-reloadable

	mu        sync.Mutex
	windows   map[string]*window
	lastSweep time.Time
	now       func() time.Time // test hook; nil means time.Now
}

type window struct {
	start time.Time
	count int
}

func (m *RateLimit) Init() error {
	m.windows = map[string]*window{}
	return nil
}

func (m *RateLimit) Handle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remaining, retryAfter, ok := m.take(ClientIP(r))
		w.Header().Set("X-RateLimit-Limit", fmt.Sprint(m.opts.Get().PerMinute))
		w.Header().Set("X-RateLimit-Remaining", fmt.Sprint(remaining))
		if !ok {
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// take consumes one request from ip's window. When the limit is reached it
// returns ok=false and the whole seconds (at least 1) until the window resets.
func (m *RateLimit) take(ip string) (remaining, retryAfter int, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	if m.now != nil {
		now = m.now()
	}
	if now.Sub(m.lastSweep) > time.Minute {
		m.sweep(now)
	}
	win := m.windows[ip]
	if win == nil || now.Sub(win.start) > time.Minute {
		win = &window{start: now}
		m.windows[ip] = win
	}
	limit := m.opts.Get().PerMinute
	if win.count >= limit {
		wait := win.start.Add(time.Minute).Sub(now)
		secs := int((wait + time.Second - 1) / time.Second)
		if secs < 1 {
			secs = 1
		}
		return 0, secs, false
	}
	win.count++
	return limit - win.count, 0, true
}

// sweep deletes windows that have expired. Callers hold m.mu.
func (m *RateLimit) sweep(now time.Time) {
	for k, win := range m.windows {
		if now.Sub(win.start) > time.Minute {
			delete(m.windows, k)
		}
	}
	m.lastSweep = now
}

var _ = bosun.Middleware[RateLimit]()
