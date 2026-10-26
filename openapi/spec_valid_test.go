package openapi

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/bluebeard63/bosun"
)

// --- fixtures covering json naming, Go 1.22 patterns and operationIds ---

type ShapeBase struct {
	ID   int    `json:"id"`
	Kind string `json:"kind"`
}

type ShapeAudit struct {
	By   string `json:"by"`
	Name string `json:"name"` // hidden by ShapeIn.Name (shallower)
}

type ShapeIn struct {
	ShapeBase
	*ShapeAudit
	Name     string   `json:"name,omitempty"`
	Nick     string   `json:",omitempty"`
	Password string   `json:"-"`
	Blob     []byte   `json:"blob"`
	Roles    []string `json:"roles"`
	Count    int      `json:"count,string"`
	secret   string   //nolint:unused // unexported fields must stay out of the schema
}

type FilesIn struct {
	Path string `path:"path"`
}

type ShapeController struct{}

var _ = bosun.Controller[ShapeController]("/shape")

func (c *ShapeController) Routes(r *bosun.Router) {
	bosun.Get(r, "/{$}", c.List)
	bosun.Post(r, "/", c.Create)
	bosun.Get(r, "/files/{path...}", c.Files)
}

func (c *ShapeController) List(ctx context.Context, _ *bosun.Req[struct{}]) ([]ShapeIn, error) {
	return nil, nil
}

func (c *ShapeController) Create(ctx context.Context, req *bosun.Req[ShapeIn]) (ShapeIn, error) {
	return req.Body, nil
}

func (c *ShapeController) Files(ctx context.Context, req *bosun.Req[FilesIn]) (string, error) {
	return req.Body.Path, nil
}

// DupController and Dup both name their operations "Dup<Method>".
type DupController struct{}
type Dup struct{}

var _ = bosun.Controller[DupController]("/dup-a")
var _ = bosun.Controller[Dup]("/dup-b")

func (c *DupController) Routes(r *bosun.Router) { bosun.Get(r, "/", c.List) }
func (c *Dup) Routes(r *bosun.Router)           { bosun.Get(r, "/", c.List) }

func (c *DupController) List(ctx context.Context, _ *bosun.Req[struct{}]) (struct{}, error) {
	return struct{}{}, nil
}

func (c *Dup) List(ctx context.Context, _ *bosun.Req[struct{}]) (struct{}, error) {
	return struct{}{}, nil
}

// --- validity checks ---

var (
	componentKey = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)
	placeholder  = regexp.MustCompile(`\{([^}]*)\}`)
	paramName    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// assertValidSpec checks the OpenAPI 3.0 structural rules client
// generators trip over. It stands in for a full validator.
func assertValidSpec(t *testing.T, spec map[string]any) {
	t.Helper()
	if spec["openapi"] != "3.0.3" {
		t.Errorf("openapi = %v", spec["openapi"])
	}
	info, _ := spec["info"].(map[string]any)
	if title, _ := info["title"].(string); title == "" {
		t.Errorf("info.title missing: %v", info)
	}
	if version, _ := info["version"].(string); version == "" {
		t.Errorf("info.version missing: %v", info)
	}
	if servers, ok := spec["servers"]; ok {
		list, _ := servers.([]any)
		if len(list) == 0 {
			t.Errorf("servers present but empty")
		}
		for _, s := range list {
			if url, _ := s.(map[string]any)["url"].(string); url == "" {
				t.Errorf("server without url: %v", s)
			}
		}
	}
	securitySchemes, _ := spec["components"].(map[string]any)["securitySchemes"].(map[string]any)
	for name, s := range securitySchemes {
		checkSecurityScheme(t, name, s.(map[string]any))
	}
	schemas, _ := spec["components"].(map[string]any)["schemas"].(map[string]any)
	for name, s := range schemas {
		if !componentKey.MatchString(name) {
			t.Errorf("component name %q is not a valid key", name)
		}
		checkSchema(t, "#/components/schemas/"+name, s, schemas)
	}

	opIDs := map[string]string{}
	for p, item := range spec["paths"].(map[string]any) {
		if !strings.HasPrefix(p, "/") || strings.Contains(p, "...") || strings.Contains(p, "$") {
			t.Errorf("path %q is not a valid OpenAPI path template", p)
		}
		var placeholders []string
		for _, m := range placeholder.FindAllStringSubmatch(p, -1) {
			if !paramName.MatchString(m[1]) {
				t.Errorf("path %q: placeholder {%s} is not a plain name", p, m[1])
			}
			placeholders = append(placeholders, m[1])
		}
		sort.Strings(placeholders)
		for method, o := range item.(map[string]any) {
			op := o.(map[string]any)
			where := method + " " + p
			id, _ := op["operationId"].(string)
			if id == "" {
				t.Errorf("%s: empty operationId", where)
			} else if prev, dup := opIDs[id]; dup {
				t.Errorf("operationId %q used by %s and %s", id, prev, where)
			}
			opIDs[id] = where

			var pathParams []string
			seen := map[string]bool{}
			params, _ := op["parameters"].([]any)
			for _, pr := range params {
				pm := pr.(map[string]any)
				name, _ := pm["name"].(string)
				in, _ := pm["in"].(string)
				if name == "" || seen[in+":"+name] {
					t.Errorf("%s: empty or duplicate parameter %s:%q", where, in, name)
				}
				seen[in+":"+name] = true
				if in == "path" {
					pathParams = append(pathParams, name)
					if pm["required"] != true {
						t.Errorf("%s: path parameter %s must be required", where, name)
					}
				}
				checkSchema(t, where+" param "+name, pm["schema"], schemas)
			}
			sort.Strings(pathParams)
			if strings.Join(pathParams, ",") != strings.Join(placeholders, ",") {
				t.Errorf("%s: path parameters %v don't match placeholders %v", where, pathParams, placeholders)
			}

			if sec, ok := op["security"]; ok {
				reqs, _ := sec.([]any)
				if len(reqs) == 0 {
					t.Errorf("%s: security present but empty", where)
				}
				for _, r := range reqs {
					for name, scopes := range r.(map[string]any) {
						scheme, ok := securitySchemes[name].(map[string]any)
						if !ok {
							t.Errorf("%s: security requirement names undefined scheme %q", where, name)
							continue
						}
						list, ok := scopes.([]any)
						if !ok {
							t.Errorf("%s: scopes for %q are %T, want an array", where, name, scopes)
						}
						typ := scheme["type"]
						if len(list) > 0 && typ != "oauth2" && typ != "openIdConnect" {
							t.Errorf("%s: %s scheme %q must have no scopes, has %v", where, typ, name, list)
						}
					}
				}
			}

			responses, _ := op["responses"].(map[string]any)
			if len(responses) == 0 {
				t.Errorf("%s: no responses", where)
			}
			for code, r := range responses {
				if r.(map[string]any)["description"] == nil {
					t.Errorf("%s %s: response without description", where, code)
				}
			}
		}
	}
}

func checkSchema(t *testing.T, where string, s any, schemas map[string]any) {
	t.Helper()
	m, ok := s.(map[string]any)
	if !ok {
		t.Errorf("%s: schema is %T", where, s)
		return
	}
	if ref, ok := m["$ref"].(string); ok {
		if schemas[strings.TrimPrefix(ref, "#/components/schemas/")] == nil {
			t.Errorf("%s: $ref %s doesn't resolve", where, ref)
		}
		if len(m) > 1 {
			t.Errorf("%s: $ref has siblings, which OpenAPI 3.0 ignores: %v", where, m)
		}
		return
	}
	if n, ok := m["nullable"]; ok {
		if n != true {
			t.Errorf("%s: nullable = %v", where, n)
		}
		if m["type"] == nil && m["allOf"] == nil {
			t.Errorf("%s: nullable without a type or allOf", where)
		}
	}
	if all, ok := m["allOf"]; ok {
		list, _ := all.([]any)
		if len(list) == 0 {
			t.Errorf("%s: empty allOf", where)
		}
		for i, s := range list {
			checkSchema(t, where+".allOf"+strconv.Itoa(i), s, schemas)
		}
	}
	if ex, ok := m["example"]; ok {
		want := map[any]string{"string": "string", "integer": "float64", "number": "float64",
			"boolean": "bool", "array": "[]interface {}", "object": "map[string]interface {}"}[m["type"]]
		if got := reflect.TypeOf(ex).String(); want != "" && got != want {
			t.Errorf("%s: example %v is %s, schema type %v", where, ex, got, m["type"])
		}
	}
	if m["type"] == "array" {
		if _, has := m["enum"]; has {
			t.Errorf("%s: enum on an array; it belongs on items", where)
		}
		checkSchema(t, where+".items", m["items"], schemas)
	}
	if props, ok := m["properties"].(map[string]any); ok {
		for name, p := range props {
			if name == "" || name == "-" {
				t.Errorf("%s: invalid property name %q", where, name)
			}
			checkSchema(t, where+"."+name, p, schemas)
		}
		req, _ := m["required"].([]any)
		for _, r := range req {
			if _, ok := props[r.(string)]; !ok {
				t.Errorf("%s: required %v is not a property", where, r)
			}
		}
	}
	if ap, ok := m["additionalProperties"]; ok {
		checkSchema(t, where+".additionalProperties", ap, schemas)
	}
}

// checkSecurityScheme checks the fields OpenAPI 3.0.3 requires per type.
func checkSecurityScheme(t *testing.T, name string, s map[string]any) {
	t.Helper()
	need := map[string][]string{
		"http":          {"scheme"},
		"apiKey":        {"in", "name"},
		"oauth2":        {"flows"},
		"openIdConnect": {"openIdConnectUrl"},
	}
	fields, ok := need[fmt.Sprint(s["type"])]
	if !ok {
		t.Errorf("security scheme %s: unknown type %v", name, s["type"])
	}
	for _, f := range fields {
		if s[f] == nil || s[f] == "" {
			t.Errorf("security scheme %s: %s requires %s", name, s["type"], f)
		}
	}
	if in := s["in"]; in != nil && in != "header" && in != "query" && in != "cookie" {
		t.Errorf("security scheme %s: in = %v", name, in)
	}
	flows, _ := s["flows"].(map[string]any)
	for fname, f := range flows {
		flow := f.(map[string]any)
		if _, ok := flow["scopes"].(map[string]any); !ok {
			t.Errorf("security scheme %s flow %s: scopes required", name, fname)
		}
		if (fname == "implicit" || fname == "authorizationCode") && flow["authorizationUrl"] == nil {
			t.Errorf("security scheme %s flow %s: authorizationUrl required", name, fname)
		}
		if fname != "implicit" && flow["tokenUrl"] == nil {
			t.Errorf("security scheme %s flow %s: tokenUrl required", name, fname)
		}
	}
}

func TestSpecIsValid(t *testing.T) {
	app := bosun.New()
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	spec := fetchSpec(t, app)
	assertValidSpec(t, spec)

	paths := spec["paths"].(map[string]any)
	opID := func(p, method string) string {
		t.Helper()
		item, ok := paths[p].(map[string]any)
		if !ok {
			t.Fatalf("path %s missing; have %v", p, reflect.ValueOf(paths).MapKeys())
		}
		return item[method].(map[string]any)["operationId"].(string)
	}
	if _, ok := paths["/shape/"]; !ok {
		t.Fatal("{$} should be dropped: /shape/ missing")
	}
	files := paths["/shape/files/{path}"].(map[string]any)["get"].(map[string]any)
	if name := files["parameters"].([]any)[0].(map[string]any)["name"]; name != "path" {
		t.Fatalf("wildcard parameter named %v, want path", name)
	}
	if got := opID("/shape/files/{path}", "get"); got != "ShapeFiles" {
		t.Fatalf("operationId = %s, want ShapeFiles", got)
	}
	// Dup and DupController collide; the later path gets the suffix.
	if a, b := opID("/dup-a", "get"), opID("/dup-b", "get"); a != "DupList" || b != "DupList2" {
		t.Fatalf("de-duplicated operationIds = %s, %s", a, b)
	}
}

func TestJSONFieldNaming(t *testing.T) {
	var names []string
	for _, f := range jsonFields(reflect.TypeOf(ShapeIn{})) {
		names = append(names, f.name)
	}
	want := []string{"id", "kind", "by", "name", "Nick", "blob", "roles", "count"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("fields = %v, want %v", names, want)
	}

	props := structSchema(reflect.TypeOf(ShapeIn{}), newSchemaSet(nil))["properties"].(map[string]any)
	if b := props["blob"].(map[string]any); b["type"] != "string" || b["format"] != "byte" {
		t.Fatalf("[]byte schema = %v", b)
	}
	if c := props["count"].(map[string]any); c["type"] != "string" {
		t.Fatalf(",string int schema = %v", c)
	}
	// bosun's validator rejects oneof on slices at Start today, so the
	// slice case is exercised on constrain directly.
	roleT := reflect.TypeOf([]string{})
	roles := constrain(schemaFor(roleT, newSchemaSet(nil)), roleT,
		[]bosun.Rule{{Name: "oneof", Param: "admin user"}}).(map[string]any)
	if _, ok := roles["enum"]; ok {
		t.Fatal("oneof on a slice must not put enum on the array")
	}
	if !reflect.DeepEqual(roles["items"].(map[string]any)["enum"], []any{"admin", "user"}) {
		t.Fatalf("items enum = %v", roles["items"])
	}

	// Same-depth conflicts: both untagged -> dropped; one tagged -> it wins.
	type A struct{ X, Y int }
	type B struct {
		X int
		Y int `json:"Y"`
	}
	type C struct {
		A
		B
	}
	names = nil
	for _, f := range jsonFields(reflect.TypeOf(C{})) {
		names = append(names, f.name+"@"+f.sf.Type.String())
	}
	if !reflect.DeepEqual(names, []string{"Y@int"}) || jsonFields(reflect.TypeOf(C{}))[0].index[0] != 1 {
		t.Fatalf("conflict resolution = %v", names)
	}
}
