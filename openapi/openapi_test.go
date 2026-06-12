package openapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"testing/fstest"
	"time"

	"github.com/amberstack/bosun"
)

// --- fixture controller exercised through a real app ---

type FixIn struct {
	ID   int    `path:"id"`
	Q    string `query:"q"`
	Body string `json:"body"`
}

type FixOut struct {
	When  time.Time       `json:"when"`
	Tags  []string        `json:"tags"`
	Meta  map[string]int  `json:"meta"`
	Inner struct{ X int } `json:"inner"`
}

type FixtureController struct{}

var _ = bosun.Controller[FixtureController]("/fix")

func (c *FixtureController) Routes(r *bosun.Router) {
	bosun.Post(r, "/thing/{id}", c.Create, bosun.Errors(http.StatusConflict))
	bosun.Get(r, "/dyn", c.Dyn)
}

func (c *FixtureController) Create(ctx context.Context, req *bosun.Req[FixIn]) (FixOut, error) {
	return FixOut{}, nil
}

func (c *FixtureController) Dyn(ctx context.Context, _ *bosun.Req[struct{}]) (struct{}, error) {
	code := 400 + 18
	return struct{}{}, bosun.E(code, "dynamic", nil)
}

// embedded "source" for the scanner: deployed-binary mode
var fixtureSrc = fstest.MapFS{
	"ctrl.go": &fstest.MapFile{Data: []byte(`package x
import "net/http"
func (c *FixtureController) Create(a, b int) error {
	if a == 0 { return bosun.E(http.StatusNotFound, "missing", nil) }
	if b == 0 { return bosun.E(422, "bad", nil) }
	return nil
}
`)},
}

var _ = Sources(fixtureSrc)

func fetchSpec(t *testing.T, app *bosun.App) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, httptest.NewRequest("GET", "/openapi.json", nil))
	if rec.Code != 200 {
		t.Fatalf("spec endpoint: %d", rec.Code)
	}
	var spec map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatal(err)
	}
	return spec
}

func responsesOf(t *testing.T, spec map[string]any, path, method string) map[string]any {
	t.Helper()
	p, ok := spec["paths"].(map[string]any)[path]
	if !ok {
		t.Fatalf("path %s missing from spec", path)
	}
	op, ok := p.(map[string]any)[method]
	if !ok {
		t.Fatalf("%s %s missing", method, path)
	}
	return op.(map[string]any)["responses"].(map[string]any)
}

func TestSpecGeneration(t *testing.T) {
	app := bosun.New()
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	spec := fetchSpec(t, app)

	resp := responsesOf(t, spec, "/fix/thing/{id}", "post")
	for _, code := range []string{"200", "400", "409", "404", "422", "500"} {
		if _, ok := resp[code]; !ok {
			t.Fatalf("missing %s (declared+scanned+defaults): have %v", code, resp)
		}
	}

	// request body, params, schemas
	op := spec["paths"].(map[string]any)["/fix/thing/{id}"].(map[string]any)["post"].(map[string]any)
	if op["requestBody"] == nil {
		t.Fatal("requestBody missing")
	}
	if op["parameters"] == nil {
		t.Fatal("path/query parameters missing")
	}
	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
	if schemas["FixIn"] == nil || schemas["FixOut"] == nil || schemas["ErrorResponse"] == nil {
		t.Fatalf("schemas missing: %v", schemas)
	}

	// observed layer: dynamic status appears only after traffic
	if _, ok := responsesOf(t, spec, "/fix/dyn", "get")["418"]; ok {
		t.Fatal("418 must not be present before traffic")
	}
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, httptest.NewRequest("GET", "/fix/dyn", nil))
	if rec.Code != 418 {
		t.Fatalf("fixture dyn route: %d", rec.Code)
	}
	spec = fetchSpec(t, app)
	if _, ok := responsesOf(t, spec, "/fix/dyn", "get")["418"]; !ok {
		t.Fatal("observed 418 should appear after traffic")
	}
}

func TestSchemaForKinds(t *testing.T) {
	schemas := map[string]any{}
	cases := map[reflect.Type]string{
		reflect.TypeOf(""):               "string",
		reflect.TypeOf(true):             "boolean",
		reflect.TypeOf(1):                "integer",
		reflect.TypeOf(1.5):              "number",
		reflect.TypeOf([]int{}):          "array",
		reflect.TypeOf(map[string]int{}): "object",
	}
	for typ, want := range cases {
		got := schemaFor(typ, schemas).(map[string]any)["type"]
		if got != want {
			t.Fatalf("%v -> %v, want %s", typ, got, want)
		}
	}
	tt := schemaFor(reflect.TypeOf(time.Time{}), schemas).(map[string]any)
	if tt["format"] != "date-time" {
		t.Fatal("time.Time should be date-time string")
	}
	// anonymous struct must inline without recursion (regression test)
	anon := schemaFor(reflect.TypeOf(struct{ A string }{}), schemas).(map[string]any)
	if anon["type"] != "object" {
		t.Fatal("anonymous struct should inline as object")
	}
	if schemaFor(reflect.TypeOf(make(chan int)), schemas).(map[string]any)["type"] != nil {
		t.Fatal("unsupported kinds yield empty schema")
	}
}

func TestPathHelpers(t *testing.T) {
	p, params := convertPath("/a/{x}/b/{y}")
	if p != "/a/{x}/b/{y}" || len(params) != 2 || params[0] != "x" || params[1] != "y" {
		t.Fatalf("convertPath wrong: %s %v", p, params)
	}
	if shortHandler("pkg.(*C).Login-fm") != "Login" {
		t.Fatal("shortHandler wrong")
	}
	if hasBodyFields(reflect.TypeOf(struct {
		A string `path:"a"`
	}{})) {
		t.Fatal("path-only struct has no body fields")
	}
	if !hasBodyFields(reflect.TypeOf(struct{ B string }{})) {
		t.Fatal("plain field is a body field")
	}
}

func TestStatusArgAndDiskScan(t *testing.T) {
	s := &errScanner{opts: &ScanOptions{SourceDir: ""}}
	s.scan() // embedded fixtureSrc only
	codes := s.statusesFor("openapi.(*FixtureController).Create-fm")
	if len(codes) != 2 || codes[0] != 404 || codes[1] != 422 {
		t.Fatalf("embedded scan wrong: %v", codes)
	}
	if s.statusesFor("nope") != nil {
		t.Fatal("unknown handler should yield nil")
	}
}
