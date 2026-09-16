package infisicalmod_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bluebeard63/bosun/modules/infisicalmod"
)

func TestLoad(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("environment") != "prod" {
			t.Errorf("environment = %q", r.URL.Query().Get("environment"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"secrets":[
			{"secretKey":"DB_PASSWORD","secretValue":"hunter2"},
			{"secretKey":"LIMITS","secretValue":"{\"perMinute\":60}"}
		]}`)
	}))
	defer srv.Close()

	src := infisicalmod.Source{APIURL: srv.URL, ProjectID: "p", Environment: "prod", Token: "tok"}
	m, err := src.Load(context.Background())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := string(m["DB_PASSWORD"]); got != `"hunter2"` {
		t.Fatalf("DB_PASSWORD = %s, want a JSON string", got)
	}
	if got := string(m["LIMITS"]); got != `{"perMinute":60}` {
		t.Fatalf("LIMITS = %s, want raw JSON object", got)
	}
}

func TestLoadError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "forbidden")
	}))
	defer srv.Close()

	src := infisicalmod.Source{APIURL: srv.URL, Token: "bad"}
	if _, err := src.Load(context.Background()); err == nil {
		t.Fatal("expected an error on 403")
	}
}
