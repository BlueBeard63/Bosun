package openapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/bluebeard63/bosun"
)

type StatusFixController struct{}

var _ = bosun.Controller[StatusFixController]("/statusfix")

func (c *StatusFixController) Routes(r *bosun.Router) {
	bosun.Post(r, "/items", c.Create, bosun.Status(http.StatusCreated))
	bosun.Delete(r, "/items/{id}", c.Remove, bosun.Status(http.StatusNoContent))
}

func (c *StatusFixController) Create(context.Context, *bosun.Req[struct{}]) (FixOut, error) {
	return FixOut{}, nil
}

func (c *StatusFixController) Remove(context.Context, *bosun.Req[struct{}]) (struct{}, error) {
	return struct{}{}, nil
}

func TestSpecSuccessStatus(t *testing.T) {
	app := bosun.New()
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	spec := fetchSpec(t, app)

	created := responsesOf(t, spec, "/statusfix/items", "post")
	if _, ok := created["200"]; ok {
		t.Fatalf("200 listed for a bosun.Status(201) route: %v", created)
	}
	r201, ok := created["201"].(map[string]any)
	if !ok || r201["description"] != "Created" || r201["content"] == nil {
		t.Fatalf("201 response = %v", created["201"])
	}

	removed := responsesOf(t, spec, "/statusfix/items/{id}", "delete")
	r204, ok := removed["204"].(map[string]any)
	if !ok || r204["description"] != "No Content" {
		t.Fatalf("204 response = %v", removed["204"])
	}
	if _, hasBody := r204["content"]; hasBody {
		t.Fatalf("204 must have no content: %v", r204)
	}
	if _, ok := removed["200"]; ok {
		t.Fatalf("200 listed for a bosun.Status(204) route: %v", removed)
	}
}
