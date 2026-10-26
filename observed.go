package bosun

import (
	"sort"
	"sync"
	"sync/atomic"
)

// --- observed statuses: every typed response records its real status ---

// statusSet records the statuses one route has returned. Recording is
// lock-free so concurrent requests never serialise on it: codes 100-611 are
// bits in an atomic bitset, and the rare codes outside it (bosun.E accepts
// up to 999) go to a sync.Map.
type statusSet struct {
	bits  [8]atomic.Uint64 // bit i is status statusBase+i
	other sync.Map         // int -> struct{}
}

const statusBase = 100

func (s *statusSet) add(code int) {
	i := code - statusBase
	if i < 0 || i >= len(s.bits)*64 {
		if _, ok := s.other.Load(code); !ok {
			s.other.Store(code, struct{}{})
		}
		return
	}
	w, bit := &s.bits[i/64], uint64(1)<<(i%64)
	for {
		// Seen codes only read, so a hot route's word stays shared in cache.
		old := w.Load()
		if old&bit != 0 || w.CompareAndSwap(old, old|bit) {
			return
		}
	}
}

// collect adds every recorded code to set.
func (s *statusSet) collect(set map[int]struct{}) {
	for wi := range s.bits {
		w := s.bits[wi].Load()
		for b := 0; b < 64; b++ {
			if w&(1<<b) != 0 {
				set[statusBase+wi*64+b] = struct{}{}
			}
		}
	}
	s.other.Range(func(k, _ any) bool {
		set[k.(int)] = struct{}{}
		return true
	})
}

// observedTable maps "METHOD /path" to each typed route's statusSet on one
// App. Sets are created when the route mounts; the request path only uses
// the set it was handed then.
type observedTable struct {
	mu     sync.Mutex
	routes map[string]*statusSet
}

// Process-wide index of every App's sets, kept for the deprecated
// package-level ObservedStatuses. It is written at mount time only and read
// lazily when that function is called.
var (
	observedMu  sync.Mutex
	observedAll = map[string][]*statusSet{}
)

// statusesFor returns the statusSet a route mounted on a records into,
// creating it on first use.
func statusesFor(a *App, method, p string) *statusSet {
	key := method + " " + p
	t := &a.observed
	t.mu.Lock()
	defer t.mu.Unlock()
	if s, ok := t.routes[key]; ok {
		return s
	}
	s := &statusSet{}
	if t.routes == nil {
		t.routes = map[string]*statusSet{}
	}
	t.routes[key] = s
	observedMu.Lock()
	observedAll[key] = append(observedAll[key], s)
	observedMu.Unlock()
	return s
}

// ObservedStatuses returns every status code the route (method + full
// pattern, as in RouteInfo) has actually returned on this app since it
// started. OpenAPI generators merge these in, so the spec keeps learning at
// runtime — including statuses no static analysis could find.
func (a *App) ObservedStatuses(method, p string) []int {
	a.observed.mu.Lock()
	s := a.observed.routes[method+" "+p]
	a.observed.mu.Unlock()
	set := map[int]struct{}{}
	if s != nil {
		s.collect(set)
	}
	return sortedCodes(set)
}

// ObservedStatuses returns the statuses a route has returned on any App in
// this process.
//
// Deprecated: use app.ObservedStatuses, which is scoped to one App. This
// package-level aggregate will be removed in a future release.
func ObservedStatuses(method, p string) []int {
	observedMu.Lock()
	sets := observedAll[method+" "+p]
	observedMu.Unlock()
	set := map[int]struct{}{}
	for _, s := range sets {
		s.collect(set)
	}
	return sortedCodes(set)
}

func sortedCodes(set map[int]struct{}) []int {
	out := make([]int, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Ints(out)
	return out
}
