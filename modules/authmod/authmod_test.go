package authmod

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/registry"
)

type hostStore struct{}

func (hostStore) FindByEmail(email string) (User, bool) {
	if email == "jack@x.dev" {
		return User{Email: email, Name: "Jack"}, true
	}
	return User{}, false
}

func TestModuleDefaults(t *testing.T) {
	app := bosun.New()
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, httptest.NewRequest("POST", "/auth/login?email=demo@example.com", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "welcome, Demo User") {
		t.Fatalf("default store/options should apply: %d %s", rec.Code, rec.Body.String())
	}
}

func TestHostOverridesExtensionPointAndOptions(t *testing.T) {
	app := bosun.New()
	registry.RegisterInstance[UserStore](app.Reg, hostStore{})
	registry.RegisterInstance[*Options](app.Reg, &Options{Greeting: "ahoy"})
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, httptest.NewRequest("POST", "/auth/login?email=jack@x.dev", nil))
	if !strings.Contains(rec.Body.String(), "ahoy, Jack") {
		t.Fatalf("host overrides not applied: %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, httptest.NewRequest("POST", "/auth/login?email=demo@example.com", nil))
	if rec.Code != 401 {
		t.Fatal("module default store should be fully replaced")
	}
	rec = httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, httptest.NewRequest("GET", "/auth/check/jack@x.dev", nil))
	if !strings.Contains(rec.Body.String(), "exists: true") {
		t.Fatal("path-param route broken")
	}
}
