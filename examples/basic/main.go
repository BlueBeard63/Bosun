package main

import (
	"fmt"
	"log"
	"net/http"

	"github.com/amberstack/bosun"
	"github.com/amberstack/bosun/registry"
)

// --- external resource, gorm.DB stand-in ---

type DB struct{ dsn string }

func (db *DB) FindUser(id int) string { return "jack" }

// --- services: no constructors anywhere ---

type UserService struct {
	db *DB // injected
}

func (s *UserService) Name(id int) string { return s.db.FindUser(id) }

var _ = bosun.Service[UserService]()

type AuthService struct {
	users    *UserService // injected
	attempts int          // plain field, left alone
}

func (s *AuthService) Init() error { // optional lifecycle hook
	s.attempts = 3
	return nil
}

func (s *AuthService) Login(user string) bool { return user == s.users.Name(1) }

var _ = bosun.Service[AuthService]()

// --- middleware: types, not strings — and they can have dependencies too ---

type LoggingMiddleware struct{}

func (m *LoggingMiddleware) Handle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Printf("[log] %s %s\n", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

var _ = bosun.Middleware[LoggingMiddleware]()

type RateLimitMiddleware struct {
	auth *AuthService // middleware with an injected service
}

func (m *RateLimitMiddleware) Handle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", fmt.Sprint(m.auth.attempts))
		next.ServeHTTP(w, r)
	})
}

var _ = bosun.Middleware[RateLimitMiddleware]()

// --- controller ---

type AuthController struct {
	auth  *AuthService // injected (unexported is fine)
	users *UserService
}

var _ = bosun.Controller[AuthController]("/auth", bosun.Use[LoggingMiddleware]())

func (c *AuthController) Routes(r *bosun.Router) {
	r.Post("/login", c.Login, bosun.Use[RateLimitMiddleware]())
	r.Get("/whoami", c.WhoAmI)
	r.Get("/users/{id}", c.User) // Go 1.22 path params work natively
}

func (c *AuthController) Login(w http.ResponseWriter, r *http.Request) {
	user := r.URL.Query().Get("user")
	if c.auth.Login(user) {
		fmt.Fprintf(w, "welcome, %s\n", user)
		return
	}
	http.Error(w, "denied", http.StatusUnauthorized)
}

func (c *AuthController) WhoAmI(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "you are %s\n", c.users.Name(1))
}

func (c *AuthController) User(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "user %s\n", r.PathValue("id"))
}

// --- main: four lines ---

func main() {
	app := bosun.New()
	registry.RegisterInstance[*DB](app.Reg, &DB{dsn: "postgres://..."})
	fmt.Println("listening on :8090")
	log.Fatal(app.Run(":8090"))
}
