package main

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/config"
	"github.com/bluebeard63/bosun/mw"
	"github.com/bluebeard63/bosun/openapi"
	"github.com/bluebeard63/bosun/registry"
)

// Embed this package's source so OpenAPI error scanning works at runtime in
// the deployed binary, even with no source on disk.
//
//go:embed *.go
var appSrc embed.FS

var _ = openapi.Sources(appSrc)

// --- custom auditor: developers bring their own sink ---
// Implement bosun.Auditor and register it; here a fake DB table.

type DatabaseAuditor struct {
	mu   sync.Mutex
	rows int
}

func (a *DatabaseAuditor) Audit(ctx context.Context, ev bosun.AuditEvent) {
	a.mu.Lock()
	a.rows++
	id := a.rows
	a.mu.Unlock()
	// in real life: INSERT INTO audit_log ... via gorm
	fmt.Printf("AUDIT->DB row=%d route=%s status=%d req=%v err=%q origin=%q\n",
		id, ev.Route, ev.Status, ev.Request, ev.Err, ev.ErrOrigin)
}

// --- hot-reloadable config: bind file key -> options type ---

var _ = config.Bind[mw.RateLimitOptions]("ratelimit")

// --- service + typed DTOs ---

type AuthService struct{}

func (s *AuthService) Check(email, password string) error {
	if email == "jack@amberstack.dev" && password == "hunter2" {
		return nil
	}
	return errors.New("no matching user record")
}

var _ = bosun.Service[AuthService]()

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	APIKey   string `json:"api_key" audit:"-"`
}

type LoginResponse struct {
	Token string `json:"token"`
	Name  string `json:"name"`
}

type UserRequest struct {
	ID int `path:"id"`
}

type UserResponse struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type AuthController struct {
	auth *AuthService
}

var _ = bosun.Controller[AuthController]("/auth")

func (c *AuthController) Routes(r *bosun.Router) {
	bosun.Post(r, "/login", c.Login, bosun.Use[mw.RateLimit]())
	// declared errors: documented even in closed-source binaries
	bosun.Get(r, "/users/{id}", c.User, bosun.Errors(http.StatusConflict))
	// dynamic status: invisible to any static analysis, caught by observation
	bosun.Get(r, "/teapot", c.Teapot)
}

func (c *AuthController) Teapot(ctx context.Context, _ *bosun.Req[struct{}]) (struct{}, error) {
	code := 400 + 18 // computed at runtime — the scanner cannot see this
	return struct{}{}, bosun.E(code, "I'm a teapot", nil)
}

func (c *AuthController) Login(ctx context.Context, req *bosun.Req[LoginRequest]) (LoginResponse, error) {
	in := req.Body
	if in.Email == "" {
		return LoginResponse{}, bosun.E(http.StatusUnprocessableEntity, "email is required", nil)
	}
	if err := c.auth.Check(in.Email, in.Password); err != nil {
		return LoginResponse{}, bosun.E(http.StatusUnauthorized, "invalid credentials", err)
	}
	return LoginResponse{Token: "secret-jwt-here", Name: "Jack"}, nil
}

func (c *AuthController) User(ctx context.Context, req *bosun.Req[UserRequest]) (UserResponse, error) {
	if req.Body.ID != 1 {
		return UserResponse{}, bosun.E(http.StatusNotFound, "user not found", nil)
	}
	return UserResponse{ID: 1, Name: "Jack"}, nil
}

// --- encrypted DB config table (stand-in for a gorm key->value table) ---

type ConfigTable struct {
	mu   sync.Mutex
	rows map[string][]byte
}

func (t *ConfigTable) All(ctx context.Context) (map[string][]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string][]byte, len(t.rows))
	for k, v := range t.rows {
		out[k] = v
	}
	return out, nil
}

func (t *ConfigTable) Put(key string, sealed []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rows[key] = sealed
}

// admin endpoint that writes encrypted config into the "table" at runtime
// (auth omitted for the demo; in real life this sits behind staff auth).

type SetLimitRequest struct {
	PerMinute int `json:"per_minute"`
}

type AdminController struct {
	table *ConfigTable
	box   *config.SecretBox
}

var _ = bosun.Controller[AdminController]("/admin")

func (c *AdminController) Routes(r *bosun.Router) {
	bosun.Post(r, "/ratelimit", c.SetLimit)
}

func (c *AdminController) SetLimit(ctx context.Context, req *bosun.Req[SetLimitRequest]) (struct{ OK bool }, error) {
	plain, _ := json.Marshal(mw.RateLimitOptions{PerMinute: req.Body.PerMinute})
	sealed, err := c.box.Seal(plain)
	if err != nil {
		return struct{ OK bool }{}, bosun.E(http.StatusInternalServerError, "seal failed", err)
	}
	c.table.Put("ratelimit", sealed) // in real life: UPDATE config SET value=? WHERE key=?
	return struct{ OK bool }{OK: true}, nil
}

func main() {
	app := bosun.New()
	registry.RegisterInstance[bosun.Auditor](app.Reg, &DatabaseAuditor{})
	registry.RegisterInstance[*config.Options](app.Reg, &config.Options{Poll: 500 * time.Millisecond})

	// encryption key: in production from KMS / env, never hardcoded
	key := sha256.Sum256([]byte(os.Getenv("CONFIG_MASTER_KEY")))
	box, err := config.NewSecretBox(key[:])
	if err != nil {
		log.Fatal(err)
	}
	table := &ConfigTable{rows: map[string][]byte{}}
	registry.RegisterInstance[*ConfigTable](app.Reg, table)
	registry.RegisterInstance[*config.SecretBox](app.Reg, box)

	// layered sources: file < env < encrypted DB table
	config.Add(config.FileSource{Path: "config.json"})
	config.Add(config.EnvSource{})
	config.Add(config.KVSource{Store: table, Decrypt: box.Open})

	fmt.Println("listening on :8094")
	log.Fatal(app.Run(":8094"))
}
