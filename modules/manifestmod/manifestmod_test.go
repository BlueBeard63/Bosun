package manifestmod_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/modules/eventmod"
	"github.com/bluebeard63/bosun/modules/manifestmod"
	"github.com/bluebeard63/bosun/registry"
)

type sampleController struct{}

var _ = bosun.Controller[sampleController]("/api")
var _ = eventmod.Declare("users.created", eventmod.Produces, "")

func (c *sampleController) Routes(r *bosun.Router) {
	bosun.Get(r, "/users/{id}", c.get)
	bosun.Post(r, "/users", c.create, bosun.Errors(http.StatusConflict))
}

type out struct {
	OK bool `json:"ok"`
}

func (c *sampleController) get(ctx context.Context, _ *bosun.Req[struct{}]) (out, error) {
	return out{OK: true}, nil
}
func (c *sampleController) create(ctx context.Context, _ *bosun.Req[struct{}]) (out, error) {
	return out{OK: true}, nil
}

func TestManifestEndpoint(t *testing.T) {
	app := bosun.New()
	registry.RegisterInstance[*manifestmod.Options](app.Reg, &manifestmod.Options{Service: "billing", Version: "v1.2.3", Port: 8080})
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, httptest.NewRequest("GET", "/.bosun/manifest", nil))
	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
	var m manifestmod.Manifest
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.Service != "billing" || m.Version != "v1.2.3" || m.Port != 8080 {
		t.Fatalf("meta = %+v", m)
	}
	if m.Health.Live != "/health/live" || m.Health.Ready != "/health/ready" {
		t.Fatalf("health = %+v", m.Health)
	}

	paths := map[string]bool{}
	for _, r := range m.Routes {
		paths[r.Method+" "+r.Path] = true
	}
	if !paths["GET /api/users/{id}"] {
		t.Fatalf("missing GET route; routes = %+v", m.Routes)
	}
	if !paths["POST /api/users"] {
		t.Fatalf("missing POST route; routes = %+v", m.Routes)
	}

	var found bool
	for _, q := range m.Queues {
		if q.Subject == "users.created" && q.Direction == "produces" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing queue declaration; queues = %+v", m.Queues)
	}
}

func TestCaddyfile(t *testing.T) {
	out := manifestmod.Caddyfile(manifestmod.Manifest{
		Service: "billing", Version: "v1", Port: 8080,
		Health: manifestmod.HealthAdvert{Ready: "/health/ready"},
	})
	if !strings.Contains(out, "reverse_proxy localhost:8080") {
		t.Fatalf("caddyfile missing reverse_proxy:\n%s", out)
	}
	if !strings.Contains(out, "# billing v1") {
		t.Fatalf("caddyfile missing header:\n%s", out)
	}
}
