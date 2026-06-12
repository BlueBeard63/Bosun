package bosun

import (
	"sort"
	"sync"
)

// --- observed statuses: every typed response records its real status ---

var (
	observedMu sync.Mutex
	observed   = map[string]map[int]struct{}{}
)

func recordObserved(method, p string, status int) {
	key := method + " " + p
	observedMu.Lock()
	if observed[key] == nil {
		observed[key] = map[int]struct{}{}
	}
	observed[key][status] = struct{}{}
	observedMu.Unlock()
}

// ObservedStatuses returns every status code this route has actually
// returned since the process started. OpenAPI generators merge these in, so
// the spec keeps learning at runtime — including statuses no static
// analysis could find.
func ObservedStatuses(method, p string) []int {
	observedMu.Lock()
	defer observedMu.Unlock()
	set := observed[method+" "+p]
	out := make([]int, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Ints(out)
	return out
}
