package bosun

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/amberstack/bosun/registry"
)

// --- shared fixtures (registrations are package-global, declared once) ---

type tSvcA struct{ created bool }

func (a *tSvcA) Init() error { a.created = true; return nil }

var _ = Service[tSvcA]()

type tSvcB struct {
	a    *tSvcA
	skip *tSvcA `inject:"-"`
	name string
}

func (b *tSvcB) Init() error { b.name = "from-init"; return nil }

var _ = Service[tSvcB]()

type tCloser struct{ closed bool }

func (c *tCloser) Close() error { c.closed = true; return nil }

var _ = Service[tCloser]()

type tIface interface{ Who() string }

type tImpl struct{}

func (tImpl) Who() string { return "default-impl" }

var _ = Service[tImpl]()
var _ = DefaultBind[tIface, tImpl]()

type tHostImpl struct{}

func (tHostImpl) Who() string { return "host-impl" }

type tMW1 struct{}

func (m *tMW1) Handle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("X-Trace", "ctrl-mw")
		next.ServeHTTP(w, r)
	})
}

var _ = Middleware[tMW1]()

type tMW2 struct{}

func (m *tMW2) Handle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("X-Trace", "route-mw")
		next.ServeHTTP(w, r)
	})
}

var _ = Middleware[tMW2]()

// badRoute lets one test mount an intentionally broken route without
// poisoning every other test's Start.
var badRoute = false

type tCtrl struct {
	b *tSvcB
}

var _ = Controller[tCtrl]("/t", Use[tMW1]())

func (c *tCtrl) Routes(r *Router) {
	r.Get("/plain", c.plain, Use[tMW2]())
	if badRoute {
		r.Get("/bad", c.plain, Use[tSvcA]()) // tSvcA is not middleware
	}
}

func (c *tCtrl) plain(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, "plain:"+c.b.name)
}

func newStarted(t *testing.T, opts ...Option) *App {
	t.Helper()
	app := New(opts...)
	if err := app.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	return app
}

func do(app *App, method, target, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)
	return rec
}

// --- injection ---

func TestInjectionAndInit(t *testing.T) {
	app := newStarted(t)
	v, err := app.Reg.ResolveType(reflect.TypeOf((*tSvcB)(nil)))
	if err != nil {
		t.Fatal(err)
	}
	b := v.(*tSvcB)
	if b.a == nil || !b.a.created {
		t.Fatal("dependency not injected or Init not called")
	}
	if b.skip != nil {
		t.Fatal(`inject:"-" field was injected`)
	}
	if b.name != "from-init" {
		t.Fatal("Init hook not called")
	}
}

func TestInjectInitFailure(t *testing.T) {
	reg := registry.New()
	type failer struct{}
	ft := reflect.TypeOf((*failInit)(nil))
	_ = failer{}
	reg.RegisterType(ft, func(r *registry.Registry) (any, error) {
		return buildInjected(r, ft)
	})
	if _, err := reg.ResolveType(ft); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected Init error, got %v", err)
	}
}

type failInit struct{}

func (f *failInit) Init() error { return errors.New("boom") }

func TestBuildInjectedNonStruct(t *testing.T) {
	reg := registry.New()
	it := reflect.TypeOf((*int)(nil))
	if _, err := buildInjected(reg, it); err == nil {
		t.Fatal("expected error for non-struct type")
	}
}

// --- defaults and binds ---

func TestDefaultBindFallback(t *testing.T) {
	app := newStarted(t)
	v, err := app.Reg.ResolveType(reflect.TypeOf((*tIface)(nil)).Elem())
	if err != nil {
		t.Fatal(err)
	}
	if v.(tIface).Who() != "default-impl" {
		t.Fatal("DefaultBind fallback not used")
	}
}

func TestDefaultBindHostWins(t *testing.T) {
	app := New()
	registry.RegisterInstance[tIface](app.Reg, tHostImpl{})
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	v, _ := app.Reg.ResolveType(reflect.TypeOf((*tIface)(nil)).Elem())
	if v.(tIface).Who() != "host-impl" {
		t.Fatal("host registration should override DefaultBind")
	}
}

// --- app options ---

func TestDisable(t *testing.T) {
	app := New(Disable("github.com/amberstack/bosun"))
	if app.Reg.Has(reflect.TypeOf((*tSvcA)(nil))) {
		t.Fatal("disabled package's services should not register")
	}
	if err := app.Start(); err != nil {
		t.Fatalf("start with everything disabled should succeed: %v", err)
	}
}

func TestOverridePrefix(t *testing.T) {
	app := newStarted(t, OverridePrefix[tCtrl]("/zz"))
	if rec := do(app, "GET", "/zz/plain", ""); rec.Code != 200 {
		t.Fatalf("remapped route not mounted: %d", rec.Code)
	}
	if rec := do(app, "GET", "/t/plain", ""); rec.Code == 200 {
		t.Fatal("original prefix should not be mounted when overridden")
	}
}

// --- router and middleware ---

func TestMiddlewareChainOrder(t *testing.T) {
	app := newStarted(t)
	rec := do(app, "GET", "/t/plain", "")
	if rec.Code != 200 || rec.Body.String() != "plain:from-init" {
		t.Fatalf("unexpected response %d %q", rec.Code, rec.Body.String())
	}
	trace := rec.Header().Values("X-Trace")
	if len(trace) != 2 || trace[0] != "ctrl-mw" || trace[1] != "route-mw" {
		t.Fatalf("middleware order wrong: %v", trace)
	}
}

func TestBadMiddlewareRefFailsStart(t *testing.T) {
	badRoute = true
	defer func() { badRoute = false }()
	app := New()
	err := app.Start()
	if err == nil || !strings.Contains(err.Error(), "does not implement Handle") {
		t.Fatalf("expected middleware type error, got %v", err)
	}
}

// --- declaration panics (must fire before any state mutates) ---

func TestMiddlewareDeclarationPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for non-middleware type")
		}
	}()
	Middleware[tSvcA]()
}

func TestControllerDeclarationPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for type without Routes")
		}
	}()
	Controller[tSvcA]("/x")
}

// --- shutdown ---

func TestShutdownClosesServices(t *testing.T) {
	app := newStarted(t)
	v, _ := app.Reg.ResolveType(reflect.TypeOf((*tCloser)(nil)))
	if err := app.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if !v.(*tCloser).closed {
		t.Fatal("Close not called on shutdown")
	}
}

// --- dynamic ---

func TestDynamic(t *testing.T) {
	d := &Dynamic[int]{}
	var seen []int
	d.OnChange(func(v *int) { seen = append(seen, *v) })
	one, two := 1, 2
	d.Set(&one)
	d.Set(&two)
	if *d.Get() != 2 || len(seen) != 2 || seen[0] != 1 || seen[1] != 2 {
		t.Fatalf("dynamic behaviour wrong: get=%d seen=%v", *d.Get(), seen)
	}
}

type tDynOpts struct{ N int }

var _ = DefaultDynamic[tDynOpts](func() *tDynOpts { return &tDynOpts{N: 9} })

func TestDefaultDynamic(t *testing.T) {
	app := newStarted(t)
	dt := reflect.TypeOf((**Dynamic[tDynOpts])(nil)).Elem()
	v, err := app.Reg.ResolveType(dt)
	if err != nil {
		t.Fatal(err)
	}
	if v.(*Dynamic[tDynOpts]).Get().N != 9 {
		t.Fatal("DefaultDynamic seed not applied")
	}
}

// --- ensure unused-import guards ---

var _ = context.Background
