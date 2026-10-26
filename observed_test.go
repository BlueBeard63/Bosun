package bosun

import (
	"context"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"testing"
)

type observedCtrl struct{}

var _ = Controller[observedCtrl]("/observed")

func (c *observedCtrl) Routes(r *Router) {
	Get(r, "/status/{code}", func(_ context.Context, req *Req[struct {
		Code int `path:"code"`
	}]) (struct{}, error) {
		if req.Body.Code != 200 {
			return struct{}{}, E(req.Body.Code, "status", nil)
		}
		return struct{}{}, nil
	})
	Get(r, "/ping", func(context.Context, *Req[struct{}]) (struct{}, error) {
		return struct{}{}, nil
	})
}

func TestObservedStatusesBitsetAndFallback(t *testing.T) {
	app := newStarted(t)
	// 100 and 611 are the edges of the bitset; 612 and 999 take the fallback.
	codes := []int{404, 200, 999, 100, 503, 404, 611, 612, 200}
	for _, code := range codes {
		do(app, "GET", "/observed/status/"+strconv.Itoa(code), "")
	}
	want := []int{100, 200, 404, 503, 611, 612, 999}
	if got := app.ObservedStatuses("GET", "/observed/status/{code}"); !slices.Equal(got, want) {
		t.Fatalf("app statuses = %v, want %v", got, want)
	}
	if got := ObservedStatuses("GET", "/observed/status/{code}"); !isSuperset(got, want) {
		t.Fatalf("deprecated aggregate = %v, want it to include %v", got, want)
	}
	if got := app.ObservedStatuses("GET", "/observed/nope"); got == nil || len(got) != 0 {
		t.Fatalf("unknown route = %#v, want empty", got)
	}
}

func TestObservedStatusesConcurrent(t *testing.T) {
	app := newStarted(t)
	var wg sync.WaitGroup
	for _, code := range []int{200, 201, 404, 418, 500, 700} {
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func(code int) {
				defer wg.Done()
				for j := 0; j < 50; j++ {
					do(app, "GET", "/observed/status/"+strconv.Itoa(code), "")
				}
			}(code)
		}
	}
	wg.Wait()
	if got := app.ObservedStatuses("GET", "/observed/status/{code}"); !slices.Equal(got, []int{200, 201, 404, 418, 500, 700}) {
		t.Fatalf("statuses = %v", got)
	}
}

// The deprecated aggregate unions every app in the process.
func TestDeprecatedObservedAggregatesApps(t *testing.T) {
	a, b := newStarted(t), newStarted(t)
	do(a, "GET", "/observed/status/409", "")
	do(b, "GET", "/observed/status/410", "")
	got := ObservedStatuses("GET", "/observed/status/{code}")
	if !isSuperset(got, []int{409, 410}) || !slices.IsSorted(got) {
		t.Fatalf("aggregate = %v", got)
	}
	if got := a.ObservedStatuses("GET", "/observed/status/{code}"); slices.Contains(got, 410) {
		t.Fatalf("app a sees app b's status: %v", got)
	}
}

func isSuperset(got, want []int) bool {
	for _, w := range want {
		if !slices.Contains(got, w) {
			return false
		}
	}
	return true
}

// BenchmarkTypedParallel serves one typed route from every core. Run with
// -cpu=8 to see contention on the request path.
func BenchmarkTypedParallel(b *testing.B) {
	app := startWithAuditor(b, nil)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest("GET", "/observed/ping", nil)
		for pb.Next() {
			app.Mux.ServeHTTP(httptest.NewRecorder(), req)
		}
	})
}
