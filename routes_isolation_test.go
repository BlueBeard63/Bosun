package bosun

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func routePaths(rs []RouteInfo) map[string]int {
	out := map[string]int{}
	for _, r := range rs {
		out[r.Method+" "+r.Path]++
	}
	return out
}

func TestTypedRoutesAreScopedToApp(t *testing.T) {
	a := newStarted(t)
	b := newStarted(t, OverridePrefix[appMWCtrl]("/other"))

	pa, pb := routePaths(a.TypedRoutes()), routePaths(b.TypedRoutes())
	if pa["GET /appmw/typed"] != 1 || pa["GET /other/typed"] != 0 {
		t.Fatalf("app A routes wrong: /appmw=%d /other=%d", pa["GET /appmw/typed"], pa["GET /other/typed"])
	}
	if pb["GET /other/typed"] != 1 || pb["GET /appmw/typed"] != 0 {
		t.Fatalf("app B routes wrong: /other=%d /appmw=%d", pb["GET /other/typed"], pb["GET /appmw/typed"])
	}
	// Every route appears exactly once per app, however many apps the
	// process has built before.
	for k, n := range pa {
		if n != 1 {
			t.Fatalf("%s mounted %d times on one app", k, n)
		}
	}
}

func TestTypedRoutesEmptyBeforeStart(t *testing.T) {
	if rs := New().TypedRoutes(); len(rs) != 0 {
		t.Fatalf("unstarted app has %d routes", len(rs))
	}
}

func TestTypedRoutesReturnsCopy(t *testing.T) {
	app := newStarted(t)
	rs := app.TypedRoutes()
	rs[0].Path = "/mutated"
	if app.TypedRoutes()[0].Path == "/mutated" {
		t.Fatal("TypedRoutes must return a copy")
	}
}

func TestObservedStatusesAreScopedToApp(t *testing.T) {
	a, b := newStarted(t), newStarted(t)
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, httptest.NewRequest("GET", "/appmw/typed", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if got := a.ObservedStatuses("GET", "/appmw/typed"); len(got) != 1 || got[0] != 200 {
		t.Fatalf("app A observed %v, want [200]", got)
	}
	if got := b.ObservedStatuses("GET", "/appmw/typed"); len(got) != 0 {
		t.Fatalf("app B observed %v from app A's traffic", got)
	}
}

func TestDeprecatedGlobalsStillAggregate(t *testing.T) {
	newStarted(t, OverridePrefix[appMWCtrl]("/legacy-agg"))
	found := false
	for _, r := range TypedRoutes() {
		if r.Path == "/legacy-agg/typed" {
			found = true
		}
	}
	if !found {
		t.Fatal("deprecated TypedRoutes() should still include every app's routes")
	}
}

func TestParallelAppsHaveIsolatedRoutes(t *testing.T) {
	for i := 0; i < 8; i++ {
		prefix := fmt.Sprintf("/par%d", i)
		t.Run(prefix, func(t *testing.T) {
			t.Parallel()
			app := newStarted(t, OverridePrefix[appMWCtrl](prefix))
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, httptest.NewRequest("GET", prefix+"/typed", nil))
			if rec.Code != 200 {
				t.Fatalf("status %d", rec.Code)
			}
			for _, r := range app.TypedRoutes() {
				if strings.Contains(r.Handler, "appMWCtrl") && r.Path != prefix+"/typed" {
					t.Fatalf("app %s sees foreign route %s", prefix, r.Path)
				}
			}
			if got := app.ObservedStatuses("GET", prefix+"/typed"); len(got) != 1 {
				t.Fatalf("observed %v", got)
			}
		})
	}
}

// appInjected checks that services can inject the *App they belong to.
type appInjected struct {
	App *App
}

var _ = Service[appInjected]()

func TestAppIsInjectable(t *testing.T) {
	app := newStarted(t)
	v, err := app.Reg.ResolveType(reflect.TypeOf((*appInjected)(nil)))
	if err != nil {
		t.Fatal(err)
	}
	if v.(*appInjected).App != app {
		t.Fatal("injected *App is not the owning app")
	}
}
