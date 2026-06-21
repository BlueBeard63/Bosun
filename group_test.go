package bosun

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// --- group fixtures ---

// recordMW tags each request with a header so tests can verify which
// middleware layers ran and in what order.
type recordMW struct{}

func (*recordMW) Handle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("X-Layer", "ctrl")
		next.ServeHTTP(w, r)
	})
}

var _ = Middleware[recordMW]()

func groupMW(tag string) MWRef {
	return UseFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Add("X-Layer", tag)
			next.ServeHTTP(w, r)
		})
	})
}

type groupCtrl struct{}

var _ = Controller[groupCtrl]("/g", Use[recordMW]())

func (c *groupCtrl) Routes(r *Router) {
	Get(r, "/root", c.ok)

	staff := r.Group("/staff", groupMW("staff"))
	Get(staff, "/list", c.ok)
	staff.Get("/raw", c.okRaw)

	v2 := staff.Group("/v2", groupMW("v2"))
	Get(v2, "/metrics", c.ok)

	// Empty prefix: middleware-only group, no extra path segment.
	guarded := r.Group("", groupMW("guarded"))
	Get(guarded, "/secret", c.ok)
}

func (c *groupCtrl) ok(ctx context.Context, _ *Req[struct{}]) (struct {
	OK bool `json:"ok"`
}, error) {
	return struct {
		OK bool `json:"ok"`
	}{true}, nil
}

func (c *groupCtrl) okRaw(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte("raw"))
}

// --- tests ---

func TestGroupExtendsPrefix(t *testing.T) {
	app := newStarted(t)

	cases := []struct {
		path string
		want []string // expected X-Layer values, in order
	}{
		{"/g/root", []string{"ctrl"}},
		{"/g/staff/list", []string{"ctrl", "staff"}},
		{"/g/staff/raw", []string{"ctrl", "staff"}},
		{"/g/staff/v2/metrics", []string{"ctrl", "staff", "v2"}},
		{"/g/secret", []string{"ctrl", "guarded"}},
	}

	for _, c := range cases {
		rec := do(app, "GET", c.path, "")
		if rec.Code != 200 {
			t.Fatalf("%s -> %d: %s", c.path, rec.Code, rec.Body.String())
		}
		got := rec.Header().Values("X-Layer")
		if len(got) != len(c.want) {
			t.Fatalf("%s: layers = %v, want %v", c.path, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("%s: layer[%d] = %q, want %q (full: %v)",
					c.path, i, got[i], c.want[i], got)
			}
		}
	}
}

func TestGroupRawRouteMounts(t *testing.T) {
	app := newStarted(t)
	rec := do(app, "GET", "/g/staff/raw", "")
	if rec.Code != 200 || rec.Body.String() != "raw" {
		t.Fatalf("raw group route: %d %q", rec.Code, rec.Body.String())
	}
}

func TestGroupEmptyPrefixDoesNotAddSegment(t *testing.T) {
	app := newStarted(t)
	rec := do(app, "GET", "/g/secret", "")
	if rec.Code != 200 {
		t.Fatalf("expected 200 at /g/secret (empty-prefix group), got %d", rec.Code)
	}
}

func TestGroupRoutesAppearInIndex(t *testing.T) {
	want := map[string]bool{
		"/g/root":              false,
		"/g/staff/list":        false,
		"/g/staff/v2/metrics":  false,
		"/g/secret":            false,
	}
	for _, rt := range TypedRoutes() {
		if _, ok := want[rt.Path]; ok {
			want[rt.Path] = true
		}
	}
	for p, found := range want {
		if !found {
			t.Fatalf("typed route index missing %s", p)
		}
	}
}

func TestGroupColonSyntaxNormalizes(t *testing.T) {
	app := newStarted(t)
	// declared with ":id" sub-prefix via TestColonGroupCtrl below; verify mount
	rec := do(app, "GET", "/cg/items/7/show", "")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"id":7`) {
		t.Fatalf("expected id=7 from path param, got: %s", rec.Body.String())
	}
}

type colonGroupCtrl struct{}

var _ = Controller[colonGroupCtrl]("/cg")

func (c *colonGroupCtrl) Routes(r *Router) {
	items := r.Group("/items/:id")
	Get(items, "/show", c.show)
}

type cgIn struct {
	ID int `path:"id"`
}

func (c *colonGroupCtrl) show(ctx context.Context, req *Req[cgIn]) (echoOut, error) {
	return echoOut{ID: req.Body.ID}, nil
}
