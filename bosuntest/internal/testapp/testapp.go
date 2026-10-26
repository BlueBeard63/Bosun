// Package testapp is a small production-shaped app used by the bosuntest
// tests: services with unexported injected fields, a DefaultBind extension
// point, a Default options provider, a session-backed auth middleware and a
// controller. The fakes live in bosuntest's _test.go files, never here.
package testapp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/bluebeard63/bosun"
)

// --- services ---

// Store is the extension point UsersService depends on.
type Store interface {
	Name(id int) (string, error)
}

// DB stands in for *gorm.DB: an external resource the host registers with
// registry.RegisterInstance in main, so tests must provide it too.
type DB struct{}

// DBStore is the production Store.
type DBStore struct {
	db *DB // injected
}

// Init fails without a database, as a real store would.
func (s *DBStore) Init() error {
	if s.db == nil {
		return errors.New("testapp: no database")
	}
	return nil
}

// Name has no rows to read.
func (s *DBStore) Name(int) (string, error) { return "", errors.New("no rows") }

var _ = bosun.Service[DBStore]()
var _ = bosun.DefaultBind[Store, DBStore]()

// Options is module configuration provided through bosun.Default.
type Options struct{ Greeting string }

var _ = bosun.Default[*Options](func() *Options { return &Options{Greeting: "hello"} })

// UsersService has unexported injected fields, so other packages can't build
// it by hand.
type UsersService struct {
	store  Store    // injected
	opts   *Options // injected
	closed bool
}

// Greet greets user id.
func (s *UsersService) Greet(id int) (string, error) {
	name, err := s.store.Name(id)
	if err != nil {
		return "", err
	}
	return s.opts.Greeting + " " + name, nil
}

// Close records shutdown.
func (s *UsersService) Close() error { s.closed = true; return nil }

// Closed reports whether Close ran.
func (s *UsersService) Closed() bool { return s.closed }

var _ = bosun.Service[UsersService]()

// ShowsService depends on the concrete *UsersService.
type ShowsService struct {
	users *UsersService // injected
}

// Users exposes the injected dependency for assertions.
func (s *ShowsService) Users() *UsersService { return s.users }

var _ = bosun.Service[ShowsService]()

// UnrelatedBuilds counts constructions of Unrelated.
var UnrelatedBuilds atomic.Int32

// Unrelated depends on nothing Build is asked for.
type Unrelated struct{}

// Init counts the construction.
func (u *Unrelated) Init() error { UnrelatedBuilds.Add(1); return nil }

var _ = bosun.Service[Unrelated]()

// --- auth ---

// AuthUser is the authenticated caller.
type AuthUser struct {
	ID   int
	Name string
}

// Sessions looks session ids up.
type Sessions interface {
	Lookup(id string) (*AuthUser, error)
}

// RedisSessions stands in for the production session store; it knows no
// sessions.
type RedisSessions struct{}

// Lookup always fails.
func (RedisSessions) Lookup(string) (*AuthUser, error) { return nil, errors.New("no session") }

var _ = bosun.Service[RedisSessions]()
var _ = bosun.DefaultBind[Sessions, RedisSessions]()

// RequireAuth reads a bearer token, looks it up and attaches the user. It has
// no test switches: tests swap its Sessions dependency instead.
type RequireAuth struct {
	Sessions Sessions // injected
}

// Handle implements bosun.MiddlewareHandler.
func (m *RequireAuth) Handle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		u, err := m.Sessions.Lookup(id)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(bosun.WithValue(r.Context(), u)))
	})
}

var _ = bosun.Middleware[RequireAuth]()

// --- controller ---

// MeOut is the /api/me response.
type MeOut struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// EchoIn is the /api/echo request.
type EchoIn struct {
	Text   string `json:"text"`
	Repeat int    `query:"repeat"`
	Trace  string `header:"X-Trace"`
}

// EchoOut is the /api/echo response.
type EchoOut struct {
	Text  string `json:"text"`
	Trace string `json:"trace"`
}

// Controller mounts the test routes under /api.
type Controller struct{}

var _ = bosun.Controller[Controller]("/api")

// Routes implements bosun.BaseController.
func (c *Controller) Routes(r *bosun.Router) {
	bosun.Get(r, "/me", c.me, bosun.Use[RequireAuth]())
	bosun.Post(r, "/echo", c.echo)
}

func (c *Controller) me(ctx context.Context, _ *bosun.Req[struct{}]) (MeOut, error) {
	u := bosun.Value[AuthUser](ctx)
	return MeOut{ID: u.ID, Name: u.Name}, nil
}

func (c *Controller) echo(_ context.Context, r *bosun.Req[EchoIn]) (EchoOut, error) {
	out := EchoOut{Trace: r.Body.Trace}
	for i := 0; i < r.Body.Repeat; i++ {
		out.Text += r.Body.Text
	}
	return out, nil
}
