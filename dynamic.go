package bosun

import (
	"sync"
	"sync/atomic"
)

// Dynamic holds a hot-reloadable value. Services inject *Dynamic[T] and call
// Get() at use-time, so config changes apply to live traffic without
// reconstructing the service. Set is safe to call from any goroutine.
type Dynamic[T any] struct {
	v    atomic.Pointer[T]
	mu   sync.Mutex
	subs []func(*T)
}

// Get returns the current value. Never returns nil if the Dynamic was
// created via DefaultDynamic or Set was called at least once.
func (d *Dynamic[T]) Get() *T { return d.v.Load() }

// Set atomically swaps the value and notifies subscribers.
func (d *Dynamic[T]) Set(v *T) {
	d.v.Store(v)
	d.mu.Lock()
	subs := append([]func(*T){}, d.subs...)
	d.mu.Unlock()
	for _, fn := range subs {
		fn(v)
	}
}

// OnChange registers a callback invoked on every Set (e.g. to resize pools,
// reopen connections, or invalidate caches when config changes).
func (d *Dynamic[T]) OnChange(fn func(*T)) {
	d.mu.Lock()
	d.subs = append(d.subs, fn)
	d.mu.Unlock()
}

// DefaultDynamic registers a fallback *Dynamic[T] seeded by build, used
// unless the host registers its own. Modules use this for hot-reloadable
// options:
//
//	var _ = bosun.DefaultDynamic[RateLimitOptions](func() *RateLimitOptions {
//		return &RateLimitOptions{PerMinute: 60}
//	})
func DefaultDynamic[T any](build func() *T) struct{} {
	return Default[*Dynamic[T]](func() *Dynamic[T] {
		d := &Dynamic[T]{}
		d.Set(build())
		return d
	})
}
