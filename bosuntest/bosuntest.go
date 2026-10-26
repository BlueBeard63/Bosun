// Package bosuntest builds Bosun services and apps inside Go tests.
//
// Build resolves one service and only its dependencies, without starting the
// app or mounting controllers:
//
//	svc := bosuntest.Build[*users.Service](t, bosuntest.With(db), bosuntest.With[users.Store](fakeStore{}))
//
// App builds, overrides and starts a whole app, and the request helpers drive
// it through app.ServeHTTP with no network:
//
//	app := bosuntest.App(t, bosuntest.With[Sessions](fakeSessions{"test-admin": admin}))
//	res := bosuntest.Get(t, app, "/me", bosuntest.Session("test-admin"))
//	me := bosuntest.Decode[MeOut](t, res)
//
// Both register app.Shutdown with t.Cleanup. Define fakes in _test.go files so
// they can never be compiled into a production binary.
package bosuntest

import (
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/registry"
)

// Option configures Build and App.
type Option func(*config)

type config struct {
	overrides []override
	appOpts   []bosun.Option
	session   sessionTransport
}

type override struct {
	typ   string
	apply func(*registry.Registry)
}

// With makes v the provider for T in the app under test. T is inferred from
// v, so pass it explicitly to bind an interface:
//
//	bosuntest.With(db)                     // *gorm.DB
//	bosuntest.With[users.Store](fakeStore{}) // interface bound with DefaultBind
//
// The override is registered before module defaults are applied, so it wins
// over bosun.Default and bosun.DefaultBind. When T is already registered (a
// bosun.Service or bosun.Middleware type), it is replaced with
// registry.Replace. Either way, every dependent receives v.
func With[T any](v T) Option {
	return func(c *config) {
		c.overrides = append(c.overrides, override{
			typ:   typeName[T](),
			apply: func(r *registry.Registry) { registry.Replace[T](r, v) },
		})
	}
}

// AppOptions passes options through to bosun.New, e.g. bosun.Disable or
// bosun.WithMiddleware.
func AppOptions(opts ...bosun.Option) Option {
	return func(c *config) { c.appOpts = append(c.appOpts, opts...) }
}

func newConfig(opts []Option) *config {
	c := &config{session: defaultSession}
	for _, o := range opts {
		o(c)
	}
	return c
}

// newApp runs bosun.New, applies the overrides and the module defaults, and
// registers Shutdown with t.Cleanup. It does not start the app.
func newApp(t testing.TB, c *config) *bosun.App {
	t.Helper()
	app := bosun.New(c.appOpts...)
	t.Cleanup(func() {
		if err := app.Shutdown(); err != nil {
			t.Errorf("bosuntest: shutdown: %v", err)
		}
	})
	for _, o := range c.overrides {
		if err := applyOverride(app.Reg, o); err != nil {
			t.Fatalf("bosuntest: With[%s]: %v", o.typ, err)
		}
	}
	app.ApplyDefaults()
	return app
}

func applyOverride(r *registry.Registry, o override) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%v", p)
		}
	}()
	o.apply(r)
	return nil
}

// Build returns the singleton T from a fresh app, constructing only T and
// what T depends on. Overrides from With are registered first and module
// defaults are applied, but the app is not started: unrelated services are
// not built and no controller is mounted. The test fails if T, or anything T
// needs, cannot be constructed. Services Build constructed are shut down
// (io.Closer) when the test ends.
func Build[T any](t testing.TB, opts ...Option) T {
	t.Helper()
	app := newApp(t, newConfig(opts))
	v, err := registry.Resolve[T](app.Reg)
	if err != nil {
		t.Fatalf("bosuntest: Build[%s]: %v", typeName[T](), err)
	}
	return v
}

// App returns a started app: bosun.New, the overrides from With, then Start,
// which applies module defaults, builds every service and mounts every
// controller. The test fails if Start returns an error. app.Shutdown runs
// when the test ends.
func App(t testing.TB, opts ...Option) *bosun.App {
	t.Helper()
	c := newConfig(opts)
	app := newApp(t, c)
	if err := app.Start(); err != nil {
		t.Fatalf("bosuntest: Start: %v", err)
	}
	apps.Store(app, c)
	t.Cleanup(func() { apps.Delete(app) })
	return app
}

// apps maps each *bosun.App made by App to its config, so request options
// such as Session can follow that app's session convention.
var apps sync.Map

func configFor(app *bosun.App) *config {
	if c, ok := apps.Load(app); ok {
		return c.(*config)
	}
	return &config{session: defaultSession}
}

func typeName[T any]() string { return reflect.TypeOf((*T)(nil)).Elem().String() }
