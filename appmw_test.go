package bosun

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- app-wide middleware fixtures ---

// traceStep returns a handler that appends name to the X-Trace header then
// calls next, so a response records the order every layer ran in.
func traceStep(name string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("X-Trace", name)
		next.ServeHTTP(w, r)
	})
}

func traceFunc(name string) MWRef {
	return UseFunc(func(next http.Handler) http.Handler { return traceStep(name, next) })
}

// appTraceMW is a registered singleton; Configure lets it be used with args.
type appTraceMW struct{}

func (*appTraceMW) Handle(next http.Handler) http.Handler { return traceStep("registered", next) }

func (*appTraceMW) Configure(name string) MiddlewareHandler {
	return MiddlewareFunc(func(next http.Handler) http.Handler { return traceStep(name, next) })
}

var _ = Middleware[appTraceMW]()

type appMWCtrl struct{}

var _ = Controller[appMWCtrl]("/appmw", traceFunc("controller"))

func (c *appMWCtrl) Routes(r *Router) {
	Get(r, "/typed", c.typed, traceFunc("route"))
	g := r.Group("/g", traceFunc("group"))
	g.Get("/raw", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("X-Trace", "handler")
	}, traceFunc("route"))
}

func (c *appMWCtrl) typed(context.Context, *Req[struct{}]) (map[string]string, error) {
	return map[string]string{"ok": "1"}, nil
}

func traceOf(app *App, method, target string) (*httptest.ResponseRecorder, string) {
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec, strings.Join(rec.Header().Values("X-Trace"), ",")
}

// --- tests ---

func TestAppMiddlewareOrdering(t *testing.T) {
	app := newStarted(t, WithMiddleware(
		Use[appTraceMW](),
		Use[appTraceMW]("configured"),
		traceFunc("inline"),
	))
	rec, got := traceOf(app, "GET", "/appmw/typed")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if want := "registered,configured,inline,controller,route"; got != want {
		t.Fatalf("trace = %q, want %q", got, want)
	}
}

func TestAppMiddlewareWrapsRawAndGroupRoutes(t *testing.T) {
	app := newStarted(t, WithMiddleware(traceFunc("app")))
	_, got := traceOf(app, "GET", "/appmw/g/raw")
	if want := "app,controller,group,route,handler"; got != want {
		t.Fatalf("trace = %q, want %q", got, want)
	}
}

func TestAppMiddlewareRepeatedOptionsAppend(t *testing.T) {
	app := newStarted(t, WithMiddleware(traceFunc("a")), WithMiddleware(traceFunc("b")))
	_, got := traceOf(app, "GET", "/appmw/typed")
	if !strings.HasPrefix(got, "a,b,controller") {
		t.Fatalf("trace = %q, want prefix a,b,controller", got)
	}
}

func TestAppMiddlewareWrapsUnmatchedAndPreflight(t *testing.T) {
	app := newStarted(t, WithMiddleware(traceFunc("app")))
	rec, got := traceOf(app, "GET", "/no/such/route")
	if rec.Code != http.StatusNotFound || got != "app" {
		t.Fatalf("404: status %d trace %q, want 404 and app", rec.Code, got)
	}
	_, got = traceOf(app, "OPTIONS", "/appmw/typed")
	if got != "app" {
		t.Fatalf("OPTIONS trace = %q, want app", got)
	}
}

func TestAppMiddlewareShortCircuit(t *testing.T) {
	deny := UseFunc(func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		})
	})
	app := newStarted(t, WithMiddleware(deny))
	rec, got := traceOf(app, "GET", "/appmw/typed")
	if rec.Code != http.StatusTeapot || got != "" {
		t.Fatalf("status %d trace %q, want 418 and no downstream layers", rec.Code, got)
	}
}

func TestAppMiddlewareBadArgsFailStart(t *testing.T) {
	app := New(WithMiddleware(Use[appTraceMW]([]int{42})))
	err := app.Start()
	if err == nil || !strings.Contains(err.Error(), "app middleware") {
		t.Fatalf("Start err = %v, want app middleware Configure error", err)
	}
}

func TestAppHandlerBeforeStartIsMux(t *testing.T) {
	app := New(WithMiddleware(traceFunc("app")))
	if app.Handler() != http.Handler(app.Mux) {
		t.Fatal("Handler() before Start should be the bare Mux")
	}
}

func TestAppWithoutMiddlewareUnchanged(t *testing.T) {
	app := newStarted(t)
	rec, got := traceOf(app, "GET", "/appmw/typed")
	if rec.Code != 200 || got != "controller,route" {
		t.Fatalf("status %d trace %q", rec.Code, got)
	}
}
