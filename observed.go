package bosun

import (
	"sort"
	"sync"
)

// --- observed statuses: every typed response records its real status ---

func (t *routeTable) observe(key string, status int) {
	t.mu.Lock()
	if t.observed == nil {
		t.observed = map[string]map[int]struct{}{}
	}
	if t.observed[key] == nil {
		t.observed[key] = map[int]struct{}{}
	}
	t.observed[key][status] = struct{}{}
	t.mu.Unlock()
}

func (t *routeTable) statuses(key string) []int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return sortedCodes(t.observed[key])
}

// Process-wide aggregate kept for the deprecated package-level ObservedStatuses.
var (
	observedMu sync.Mutex
	observed   = map[string]map[int]struct{}{}
)

func recordObserved(a *App, method, p string, status int) {
	key := method + " " + p
	a.routes.observe(key, status)
	observedMu.Lock()
	if observed[key] == nil {
		observed[key] = map[int]struct{}{}
	}
	observed[key][status] = struct{}{}
	observedMu.Unlock()
}

// ObservedStatuses returns every status code the route (method + full
// pattern, as in RouteInfo) has actually returned on this app since it
// started. OpenAPI generators merge these in, so the spec keeps learning at
// runtime — including statuses no static analysis could find.
func (a *App) ObservedStatuses(method, p string) []int {
	return a.routes.statuses(method + " " + p)
}

// ObservedStatuses returns the statuses a route has returned on any App in
// this process.
//
// Deprecated: use app.ObservedStatuses, which is scoped to one App. This
// package-level aggregate will be removed in a future release.
func ObservedStatuses(method, p string) []int {
	observedMu.Lock()
	defer observedMu.Unlock()
	return sortedCodes(observed[method+" "+p])
}

func sortedCodes(set map[int]struct{}) []int {
	out := make([]int, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Ints(out)
	return out
}
