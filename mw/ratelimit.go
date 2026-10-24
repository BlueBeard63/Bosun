package mw

import (
	"fmt"
	"net"
	"net/http"
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

// RateLimit is a fixed-window per-IP limiter. For multi-node deployments
// replace it with a shared-store implementation. Expired windows are swept
// from the map at most once per window length, so idle clients don't
// accumulate.
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
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		remaining, ok := m.take(ip)
		w.Header().Set("X-RateLimit-Limit", fmt.Sprint(m.opts.Get().PerMinute))
		w.Header().Set("X-RateLimit-Remaining", fmt.Sprint(remaining))
		if !ok {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (m *RateLimit) take(ip string) (remaining int, ok bool) {
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
		return 0, false
	}
	win.count++
	return limit - win.count, true
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
