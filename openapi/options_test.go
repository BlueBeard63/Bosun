package openapi

import (
	"context"
	"reflect"
	"testing"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/registry"
)

// --- fixtures: permissions, pointer fields, examples ---

type SecNote struct {
	Title  string    `json:"title" example:"Groceries"`
	Pages  int       `json:"pages" example:"12"`
	Draft  bool      `json:"draft" example:"true"`
	Tags   []string  `json:"tags" example:"[\"home\",\"weekly\"]"`
	Parent *SecNote  `json:"parent"`
	Due    *string   `json:"due" example:"2024-05-01"`
	Owner  SecOwner  `json:"owner" example:"{\"name\":\"ann\"}"`
	Notes  []SecNote `json:"notes,omitempty"`
}

type SecOwner struct {
	Name string `json:"name"`
}

type SecQuery struct {
	Limit int `query:"limit" example:"20"`
}

type SecController struct{}

var _ = bosun.Controller[SecController]("/sec",
	bosun.Meta(bosun.MetaPermissions, []string{"notes:read"}))

func (c *SecController) Routes(r *bosun.Router) {
	bosun.Get(r, "/notes", c.List)
	bosun.Post(r, "/notes", c.Create, bosun.Meta(bosun.MetaPermissions, []string{"notes:write", "notes:read"}))
}

func (c *SecController) List(ctx context.Context, _ *bosun.Req[SecQuery]) ([]SecNote, error) {
	return nil, nil
}

func (c *SecController) Create(ctx context.Context, req *bosun.Req[SecNote]) (SecNote, error) {
	return req.Body, nil
}

func opOf(t *testing.T, spec map[string]any, path, method string) map[string]any {
	t.Helper()
	item, ok := spec["paths"].(map[string]any)[path].(map[string]any)
	if !ok {
		t.Fatalf("path %s missing", path)
	}
	op, ok := item[method].(map[string]any)
	if !ok {
		t.Fatalf("%s %s missing", method, path)
	}
	return op
}

func TestSpecDefaultInfo(t *testing.T) {
	app := bosun.New()
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	spec := fetchSpec(t, app)
	if !reflect.DeepEqual(spec["info"], map[string]any{"title": "bosun API", "version": "1.0.0"}) {
		t.Fatalf("default info = %v", spec["info"])
	}
	if _, ok := spec["servers"]; ok {
		t.Fatal("no servers configured, none expected")
	}
	if _, ok := spec["components"].(map[string]any)["securitySchemes"]; ok {
		t.Fatal("no schemes configured, none expected")
	}
	op := opOf(t, spec, "/sec/notes", "post")
	if _, ok := op["security"]; ok {
		t.Fatal("security must not reference unconfigured schemes")
	}
	if !reflect.DeepEqual(op["x-permissions"], []any{"notes:read", "notes:write"}) {
		t.Fatalf("x-permissions = %v", op["x-permissions"])
	}
	if _, ok := opOf(t, spec, "/fix/dyn", "get")["x-permissions"]; ok {
		t.Fatal("route without permissions has x-permissions")
	}
	assertValidSpec(t, spec)
}

func TestSpecOptions(t *testing.T) {
	app := bosun.New()
	registry.RegisterInstance[*Options](app.Reg, &Options{
		Title:       "Notes API",
		Version:     "2.1.0",
		Description: "Keeps notes.",
		Servers: []Server{
			{URL: "https://api.example.com", Description: "production"},
			{URL: "/"},
		},
		SecuritySchemes: map[string]SecurityScheme{
			"bearer": {Type: "http", Scheme: "bearer", BearerFormat: "JWT"},
			"oauth": {Type: "oauth2", Flows: OAuthFlows{ClientCredentials: &OAuthFlow{
				TokenURL: "https://auth.example.com/token",
				Scopes:   map[string]string{"notes:read": "Read notes"},
			}}},
			"key": {Type: "apiKey", In: "header", Name: "X-API-Key"},
		},
		PermissionSchemes: []string{"bearer", "oauth", "missing"},
	})
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	spec := fetchSpec(t, app)
	assertValidSpec(t, spec)

	if !reflect.DeepEqual(spec["info"], map[string]any{
		"title": "Notes API", "version": "2.1.0", "description": "Keeps notes.",
	}) {
		t.Fatalf("info = %v", spec["info"])
	}
	wantServers := []any{
		map[string]any{"url": "https://api.example.com", "description": "production"},
		map[string]any{"url": "/"},
	}
	if !reflect.DeepEqual(spec["servers"], wantServers) {
		t.Fatalf("servers = %v", spec["servers"])
	}

	schemes := spec["components"].(map[string]any)["securitySchemes"].(map[string]any)
	if !reflect.DeepEqual(schemes["bearer"], map[string]any{"type": "http", "scheme": "bearer", "bearerFormat": "JWT"}) {
		t.Fatalf("bearer scheme = %v", schemes["bearer"])
	}
	if !reflect.DeepEqual(schemes["key"], map[string]any{"type": "apiKey", "in": "header", "name": "X-API-Key"}) {
		t.Fatalf("apiKey scheme = %v", schemes["key"])
	}
	flow := schemes["oauth"].(map[string]any)["flows"].(map[string]any)["clientCredentials"].(map[string]any)
	if !reflect.DeepEqual(flow["scopes"], map[string]any{"notes:read": "Read notes", "notes:write": ""}) {
		t.Fatalf("oauth scopes should list every permission, keeping descriptions: %v", flow["scopes"])
	}

	// A route with permissions requires the listed schemes as alternatives:
	// scopes for oauth2, none for http (OpenAPI 3.0 allows none there).
	want := []any{
		map[string]any{"bearer": []any{}},
		map[string]any{"oauth": []any{"notes:read", "notes:write"}},
	}
	if got := opOf(t, spec, "/sec/notes", "post")["security"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("security = %v, want %v", got, want)
	}
	if got := opOf(t, spec, "/sec/notes", "get")["security"]; !reflect.DeepEqual(got, []any{
		map[string]any{"bearer": []any{}},
		map[string]any{"oauth": []any{"notes:read"}},
	}) {
		t.Fatalf("controller-level permission: security = %v", got)
	}
	if _, ok := opOf(t, spec, "/fix/dyn", "get")["security"]; ok {
		t.Fatal("a route without permissions has no security requirement")
	}
}

func TestSpecNullableAndExamples(t *testing.T) {
	app := bosun.New()
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	spec := fetchSpec(t, app)
	note := spec["components"].(map[string]any)["schemas"].(map[string]any)["OpenapiSecNote"].(map[string]any)
	props := note["properties"].(map[string]any)

	// *string: inline schema gains nullable; *SecNote: $ref wrapped in allOf.
	if due := props["due"].(map[string]any); due["type"] != "string" || due["nullable"] != true || due["example"] != "2024-05-01" {
		t.Fatalf("due = %v", due)
	}
	if parent := props["parent"].(map[string]any); parent["nullable"] != true || parent["type"] != "object" ||
		!reflect.DeepEqual(parent["allOf"], []any{map[string]any{"$ref": "#/components/schemas/OpenapiSecNote"}}) {
		t.Fatalf("parent = %v", parent)
	}
	if _, ok := props["title"].(map[string]any)["nullable"]; ok {
		t.Fatal("non-pointer field marked nullable")
	}

	examples := map[string]any{
		"title": "Groceries",
		"pages": float64(12),
		"draft": true,
		"tags":  []any{"home", "weekly"},
		"owner": map[string]any{"name": "ann"},
	}
	for name, want := range examples {
		if got := props[name].(map[string]any)["example"]; !reflect.DeepEqual(got, want) {
			t.Errorf("%s example = %#v, want %#v", name, got, want)
		}
	}
	if _, ok := props["notes"].(map[string]any)["example"]; ok {
		t.Fatal("field without an example tag has an example")
	}

	params := opOf(t, spec, "/sec/notes", "get")["parameters"].([]any)
	if s := params[0].(map[string]any)["schema"].(map[string]any); s["example"] != float64(20) {
		t.Fatalf("query parameter example = %v", s)
	}
	assertValidSpec(t, spec)
}
