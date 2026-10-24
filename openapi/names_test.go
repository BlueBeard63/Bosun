package openapi

import (
	"context"
	htmltemplate "html/template"
	"net/http/httptest"
	"reflect"
	"testing"
	texttemplate "text/template"

	"github.com/bluebeard63/bosun"
	v1users "github.com/bluebeard63/bosun/openapi/internal/oafix/v1/users"
	v2users "github.com/bluebeard63/bosun/openapi/internal/oafix/v2/users"
)

type Page[T any] struct {
	Items []T `json:"items"`
}

type RemoveIn struct {
	ID     int    `path:"id"`
	Reason string `json:"reason"`
}

type CollideController struct{}

var _ = bosun.Controller[CollideController]("/collide")

func (c *CollideController) Routes(r *bosun.Router) {
	bosun.Get(r, "/v1", c.V1)
	bosun.Get(r, "/v2", c.V2)
	bosun.Delete(r, "/items/{id}", c.Remove)
	bosun.Delete(r, "/plain/{id}", c.RemovePlain)
}

func (c *CollideController) V1(ctx context.Context, _ *bosun.Req[struct{}]) (v1users.Out, error) {
	return v1users.Out{}, nil
}

func (c *CollideController) V2(ctx context.Context, _ *bosun.Req[struct{}]) (v2users.Out, error) {
	return v2users.Out{}, nil
}

func (c *CollideController) Remove(ctx context.Context, _ *bosun.Req[RemoveIn]) (struct{}, error) {
	return struct{}{}, nil
}

func (c *CollideController) RemovePlain(ctx context.Context, _ *bosun.Req[FilesIn]) (struct{}, error) {
	return struct{}{}, nil
}

func TestSchemaNameCollisions(t *testing.T) {
	app := bosun.New()
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	spec := fetchSpec(t, app)
	assertValidSpec(t, spec)
	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
	paths := spec["paths"].(map[string]any)

	outRef := func(p string) string {
		op := paths[p].(map[string]any)["get"].(map[string]any)
		ok := op["responses"].(map[string]any)["200"].(map[string]any)
		return ok["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["$ref"].(string)
	}
	cases := map[string]struct{ ref, prop string }{
		"/collide/v1": {"#/components/schemas/V1UsersOut", "name"},
		"/collide/v2": {"#/components/schemas/V2UsersOut", "full_name"},
	}
	for p, want := range cases {
		if got := outRef(p); got != want.ref {
			t.Fatalf("%s 200 $ref = %s, want %s", p, got, want.ref)
		}
		props := schemas[want.ref[len("#/components/schemas/"):]].(map[string]any)["properties"].(map[string]any)
		if _, ok := props[want.prop]; !ok || len(props) != map[string]int{"name": 1, "full_name": 2}[want.prop] {
			t.Fatalf("%s points at the wrong schema: %v", p, props)
		}
	}

	del := paths["/collide/items/{id}"].(map[string]any)["delete"].(map[string]any)
	body, ok := del["requestBody"].(map[string]any)
	if !ok {
		t.Fatal("DELETE with body fields should document its requestBody")
	}
	ref := body["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["$ref"]
	if ref != "#/components/schemas/OpenapiRemoveIn" {
		t.Fatalf("DELETE requestBody $ref = %v", ref)
	}
	if _, ok := paths["/collide/plain/{id}"].(map[string]any)["delete"].(map[string]any)["requestBody"]; ok {
		t.Fatal("DELETE without body fields must not have a requestBody")
	}
}

func TestSchemaNamesDeterministic(t *testing.T) {
	types := []reflect.Type{
		reflect.TypeOf(htmltemplate.Template{}),
		reflect.TypeOf(texttemplate.Template{}),
		reflect.TypeOf(Page[v1users.Out]{}),
		reflect.TypeOf(Page[v2users.Out]{}),
		reflect.TypeOf(v1users.Out{}),
		reflect.TypeOf(FixOut{}),
	}
	want := map[reflect.Type]string{
		types[0]: "HtmlTemplateTemplate",
		types[1]: "TextTemplateTemplate",
		// Same package and sanitized name: only a numeric suffix can
		// separate them, assigned in type name order.
		types[2]: "GithubComBluebeard63BosunOpenapiPageUsersOut",
		types[3]: "GithubComBluebeard63BosunOpenapiPageUsersOut2",
		types[4]: "UsersOut",
		types[5]: "OpenapiFixOut",
	}
	for i := 0; i < 10; i++ {
		shuffled := append([]reflect.Type(nil), types...)
		for j := range shuffled {
			k := (j*7 + i) % len(shuffled)
			shuffled[j], shuffled[k] = shuffled[k], shuffled[j]
		}
		if got := schemaNames(shuffled); !reflect.DeepEqual(got, want) {
			t.Fatalf("schemaNames = %v, want %v", got, want)
		}
	}
}

func TestSpecBuiltOnce(t *testing.T) {
	app := bosun.New()
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	c := &SpecController{app: app, scanner: &errScanner{opts: &ScanOptions{}}}
	first := c.document()
	for i := 0; i < 3; i++ {
		if got := c.document(); &got[0] != &first[0] || c.builds != 1 {
			t.Fatalf("request %d rebuilt the spec (builds=%d)", i, c.builds)
		}
	}
	// A newly observed status is the one thing that changes the document.
	app.Mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/fix/dyn", nil))
	c.document()
	c.document()
	if c.builds != 2 {
		t.Fatalf("builds = %d after a new observed status, want 2", c.builds)
	}
}
