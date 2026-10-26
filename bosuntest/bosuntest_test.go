package bosuntest_test

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/bosuntest"
	"github.com/bluebeard63/bosun/bosuntest/internal/testapp"
)

// --- fakes: defined in _test.go so they can never ship ---

type fakeStore map[int]string

func (s fakeStore) Name(id int) (string, error) {
	if n, ok := s[id]; ok {
		return n, nil
	}
	return "", errors.New("not found")
}

// fakeSessions is a test-only session table. The real RequireAuth middleware
// looks ids up in it exactly as it would in Redis.
type fakeSessions map[string]*testapp.AuthUser

func (s fakeSessions) Lookup(id string) (*testapp.AuthUser, error) {
	if u, ok := s[id]; ok {
		return u, nil
	}
	return nil, errors.New("no session")
}

var admin = &testapp.AuthUser{ID: 1, Name: "admin"}

// db provides the external resource main would register.
var db = bosuntest.With(&testapp.DB{})

// --- Build ---

func TestBuildServiceFromAnotherPackage(t *testing.T) {
	before := testapp.UnrelatedBuilds.Load()

	svc := bosuntest.Build[*testapp.UsersService](t, bosuntest.With[testapp.Store](fakeStore{7: "jack"}))

	got, err := svc.Greet(7)
	if err != nil || got != "hello jack" { // "hello" comes from bosun.Default[*Options]
		t.Fatalf("Greet = %q, %v", got, err)
	}
	if n := testapp.UnrelatedBuilds.Load(); n != before {
		t.Fatalf("Build constructed an unrelated service (%d builds)", n-before)
	}
}

func TestBuildOverridesDefault(t *testing.T) {
	svc := bosuntest.Build[*testapp.UsersService](t,
		bosuntest.With[testapp.Store](fakeStore{7: "jack"}),
		bosuntest.With(&testapp.Options{Greeting: "hi"}),
	)
	if got, _ := svc.Greet(7); got != "hi jack" {
		t.Fatalf("Greet = %q; want the overridden Options", got)
	}
}

func TestBuildInjectsReplacedServiceIntoDependents(t *testing.T) {
	users := bosuntest.Build[*testapp.UsersService](t, bosuntest.With[testapp.Store](fakeStore{}))
	shows := bosuntest.Build[*testapp.ShowsService](t, bosuntest.With(users))
	if shows.Users() != users {
		t.Fatal("ShowsService did not receive the replaced *UsersService")
	}
}

func TestBuildShutsDownOnCleanup(t *testing.T) {
	var svc *testapp.UsersService
	t.Run("inner", func(t *testing.T) {
		svc = bosuntest.Build[*testapp.UsersService](t, bosuntest.With[testapp.Store](fakeStore{}))
		if svc.Closed() {
			t.Fatal("closed too early")
		}
	})
	if !svc.Closed() {
		t.Fatal("service not shut down by t.Cleanup")
	}
}

func TestBuildFailsOnMissingDependency(t *testing.T) {
	// Without a Store override the default DBStore can't be built.
	ft := runFake(func(t testing.TB) { bosuntest.Build[*testapp.UsersService](t) })
	if !ft.failed || !strings.Contains(ft.msg, "no database") {
		t.Fatalf("failed=%v msg=%q; want a Build failure naming the cause", ft.failed, ft.msg)
	}
}

func TestBuildFailsOnUnregisteredType(t *testing.T) {
	type nothing struct{}
	ft := runFake(func(t testing.TB) { bosuntest.Build[*nothing](t) })
	if !ft.failed || !strings.Contains(ft.msg, "no provider registered") {
		t.Fatalf("failed=%v msg=%q", ft.failed, ft.msg)
	}
}

// --- App and requests ---

func TestRouteInThreeLines(t *testing.T) {
	app := bosuntest.App(t, db)
	res := bosuntest.Post(t, app, "/api/echo", bosuntest.JSON(testapp.EchoIn{Text: "ab"}),
		bosuntest.Query("repeat", "2"), bosuntest.Header("X-Trace", "t1"))
	out := bosuntest.Decode[testapp.EchoOut](t, res)

	if res.Status != http.StatusOK || out.Text != "abab" || out.Trace != "t1" {
		t.Fatalf("status %d, out %+v, body %s", res.Status, out, res)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q", ct)
	}
}

// The recommended pattern: the real RequireAuth runs, but its Sessions
// dependency (bound with DefaultBind in production) is a fake from this file.
func TestSessionThroughRealAuthMiddleware(t *testing.T) {
	app := bosuntest.App(t, db,
		bosuntest.With[testapp.Sessions](fakeSessions{"test-admin": admin}),
	)

	res := bosuntest.Get(t, app, "/api/me", bosuntest.Session("test-admin"))
	if res.Status != http.StatusOK {
		t.Fatalf("status %d: %s", res.Status, res)
	}
	if me := bosuntest.Decode[testapp.MeOut](t, res); me.Name != "admin" {
		t.Fatalf("me = %+v", me)
	}

	// The middleware's real 401 paths still run.
	if res := bosuntest.Get(t, app, "/api/me", bosuntest.Session("nobody")); res.Status != http.StatusUnauthorized {
		t.Fatalf("unknown session: status %d", res.Status)
	}
	if res := bosuntest.Get(t, app, "/api/me"); res.Status != http.StatusUnauthorized {
		t.Fatalf("no session: status %d", res.Status)
	}
}

func TestSessionWithoutOverrideUsesProductionStore(t *testing.T) {
	app := bosuntest.App(t, db)
	if res := bosuntest.Get(t, app, "/api/me", bosuntest.Session("test-admin")); res.Status != http.StatusUnauthorized {
		t.Fatalf("status %d; the production store knows no test sessions", res.Status)
	}
}

func TestSessionCookieAndHeaderConventions(t *testing.T) {
	var seen *http.Request
	capture := bosun.UseFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = r
			next.ServeHTTP(w, r)
		})
	})
	mw := bosuntest.AppOptions(bosun.WithMiddleware(capture))

	app := bosuntest.App(t, db, mw, bosuntest.SessionCookie("sid"))
	bosuntest.Get(t, app, "/api/echo", bosuntest.Session("abc"))
	if c, err := seen.Cookie("sid"); err != nil || c.Value != "abc" {
		t.Fatalf("cookie = %v, %v", c, err)
	}

	app = bosuntest.App(t, db, mw, bosuntest.SessionHeader("X-Session", ""))
	bosuntest.Get(t, app, "/api/echo", bosuntest.Session("abc"))
	if got := seen.Header.Get("X-Session"); got != "abc" {
		t.Fatalf("X-Session = %q", got)
	}
}

// Overriding the middleware type itself works too, via registry.Replace,
// though swapping its dependency (above) is preferred.
func TestOverrideMiddlewareType(t *testing.T) {
	app := bosuntest.App(t, db,
		bosuntest.With(&testapp.RequireAuth{Sessions: fakeSessions{"x": admin}}),
	)
	if res := bosuntest.Get(t, app, "/api/me", bosuntest.Session("x")); res.Status != http.StatusOK {
		t.Fatalf("status %d: %s", res.Status, res)
	}
}

func TestAppFailsOnStartError(t *testing.T) {
	ft := runFake(func(t testing.TB) { bosuntest.App(t) }) // no DB: DBStore can't build
	if !ft.failed || !strings.Contains(ft.msg, "Start") {
		t.Fatalf("failed=%v msg=%q", ft.failed, ft.msg)
	}
}

func TestDoAndQueryMerge(t *testing.T) {
	app := bosuntest.App(t, db)
	req, _ := http.NewRequest(http.MethodPost, "/api/echo?repeat=3", strings.NewReader(`{"text":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	if out := bosuntest.Decode[testapp.EchoOut](t, bosuntest.Do(t, app, req)); out.Text != "xxx" {
		t.Fatalf("Do: out %+v", out)
	}
	res := bosuntest.Post(t, app, "/api/echo?repeat=1", bosuntest.JSON(map[string]string{"text": "y"}),
		bosuntest.Query("other", "1"))
	if out := bosuntest.Decode[testapp.EchoOut](t, res); out.Text != "y" {
		t.Fatalf("path query lost: out %+v", out)
	}
}

func TestDecodeFailsOnInvalidJSON(t *testing.T) {
	ft := runFake(func(t testing.TB) {
		bosuntest.Decode[testapp.EchoOut](t, &bosuntest.Response{Status: 500, Body: []byte("oops")})
	})
	if !ft.failed || !strings.Contains(ft.msg, "oops") {
		t.Fatalf("failed=%v msg=%q", ft.failed, ft.msg)
	}
}

// --- a fake testing.TB to observe failures without failing this test ---

type fakeTB struct {
	testing.TB
	failed   bool
	msg      string
	cleanups []func()
}

func (f *fakeTB) Helper() {}
func (f *fakeTB) Fatalf(format string, args ...any) {
	f.failed = true
	f.msg = fmt.Sprintf(format, args...)
	panic(fatal{})
}
func (f *fakeTB) Errorf(format string, args ...any) {
	f.failed = true
	f.msg = fmt.Sprintf(format, args...)
}
func (f *fakeTB) Cleanup(fn func()) { f.cleanups = append(f.cleanups, fn) }

type fatal struct{}

func runFake(fn func(testing.TB)) *fakeTB {
	ft := &fakeTB{}
	func() {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(fatal); !ok {
					panic(r)
				}
			}
		}()
		fn(ft)
	}()
	for i := len(ft.cleanups) - 1; i >= 0; i-- {
		ft.cleanups[i]()
	}
	return ft
}
