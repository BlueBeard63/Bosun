package openapi

import (
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/registry"
)

func get(t *testing.T, app *bosun.App, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

var specURLAttr = regexp.MustCompile(`data-spec-url="([^"]*)"`)
var scriptSrc = regexp.MustCompile(`<script src="([^"]*)"></script>`)

// followDocs fetches the docs page at page and checks that the spec and
// init script it references, resolved against the page URL, are served.
func followDocs(t *testing.T, app *bosun.App, page string) string {
	t.Helper()
	rec := get(t, app, page)
	if rec.Code != 200 {
		t.Fatalf("GET %s: %d", page, rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("GET %s: content type %q", page, ct)
	}
	body := rec.Body.String()
	base, _ := url.Parse("http://host" + page)
	m := specURLAttr.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("page has no spec URL:\n%s", body)
	}
	spec := base.ResolveReference(&url.URL{Path: m[1]}).Path
	if rec := get(t, app, spec); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"openapi": "3.0.3"`) {
		t.Fatalf("spec URL %q (resolved %s) from %s: %d", m[1], spec, page, rec.Code)
	}
	scripts := scriptSrc.FindAllStringSubmatch(body, -1)
	if len(scripts) != 2 {
		t.Fatalf("want bundle and init scripts, got %v", scripts)
	}
	init := base.ResolveReference(&url.URL{Path: scripts[1][1]}).Path
	if rec := get(t, app, init); rec.Code != 200 || !strings.Contains(rec.Body.String(), "SwaggerUIBundle") {
		t.Fatalf("init script %q (resolved %s): %d", scripts[1][1], init, rec.Code)
	}
	if strings.Contains(body, "<script>") {
		t.Fatal("page must have no inline script (strict CSP)")
	}
	return body
}

func TestDocsPageDefault(t *testing.T) {
	app := bosun.New()
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	body := followDocs(t, app, "/docs")
	for _, want := range []string{
		`<title>bosun API</title>`,
		`href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui.css"`,
		`src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui-bundle.js"`,
		`data-spec-url="./openapi.json"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %s:\n%s", want, body)
		}
	}

	// Raw routes: listed by app.Routes, absent from the spec.
	raw := map[string]bool{}
	for _, rt := range app.Routes() {
		if rt.Raw {
			raw[rt.Path] = true
		}
	}
	if !raw["/docs"] || !raw["/docs/init.js"] {
		t.Fatalf("docs routes should be raw routes: %v", raw)
	}
	for p := range fetchSpec(t, app)["paths"].(map[string]any) {
		if strings.HasPrefix(p, "/docs") {
			t.Fatalf("docs route %s is in the spec", p)
		}
	}
}

func TestDocsPageOptions(t *testing.T) {
	app := bosun.New(bosun.OverridePrefix[SpecController]("/v1"))
	registry.RegisterInstance[*DocsOptions](app.Reg, &DocsOptions{
		Path:      "/api/reference/",
		Title:     `Notes <API> & co`,
		AssetsURL: "/static/swagger/",
	})
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	body := followDocs(t, app, "/v1/api/reference/")
	for _, want := range []string{
		`<title>Notes &lt;API&gt; &amp; co</title>`,
		`href="/static/swagger/swagger-ui.css"`,
		`src="/static/swagger/swagger-ui-bundle.js"`,
		`data-spec-url="../../openapi.json"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %s:\n%s", want, body)
		}
	}
	if rec := get(t, app, "/docs"); rec.Code != 404 {
		t.Fatalf("default path still served: %d", rec.Code)
	}
	if rec := get(t, app, "/v1/api/reference/other"); rec.Code != 404 {
		t.Fatalf("trailing-slash path must match exactly: %d", rec.Code)
	}

	// Behind a proxy that strips /gateway, the browser sees the page at
	// /gateway/v1/api/reference/; the relative spec URL still resolves to
	// /gateway/v1/openapi.json, which the proxy forwards as /v1/openapi.json.
	base, _ := url.Parse("http://host/gateway/v1/api/reference/")
	if got := base.ResolveReference(&url.URL{Path: "../../openapi.json"}).Path; got != "/gateway/v1/openapi.json" {
		t.Fatalf("resolved behind proxy: %s", got)
	}
}

func TestDocsPageTitleFromOptions(t *testing.T) {
	app := bosun.New()
	registry.RegisterInstance[*Options](app.Reg, &Options{Title: "Notes API"})
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	if body := followDocs(t, app, "/docs"); !strings.Contains(body, "<title>Notes API</title>") {
		t.Fatalf("title should default to Options.Title:\n%s", body)
	}
}

func TestDocsPageDisabled(t *testing.T) {
	app := bosun.New()
	registry.RegisterInstance[*DocsOptions](app.Reg, &DocsOptions{})
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	if rec := get(t, app, "/docs"); rec.Code != 404 {
		t.Fatalf("empty Path should disable the page: %d", rec.Code)
	}
	if rec := get(t, app, "/docs/init.js"); rec.Code != 404 {
		t.Fatalf("empty Path should disable the script: %d", rec.Code)
	}
	fetchSpec(t, app) // still served
}

func TestRelativeURL(t *testing.T) {
	cases := map[[2]string]string{
		{"/docs", "/openapi.json"}:           "./openapi.json",
		{"/", "/openapi.json"}:               "./openapi.json",
		{"/docs/", "/openapi.json"}:          "../openapi.json",
		{"/a/b/docs", "/openapi.json"}:       "../../openapi.json",
		{"/docs", "/docs/init.js"}:           "./docs/init.js",
		{"/docs/", "/docs/init.js"}:          "../docs/init.js",
		{"/api/reference/", "/openapi.json"}: "../../openapi.json",
	}
	for in, want := range cases {
		if got := relativeURL(in[0], in[1]); got != want {
			t.Errorf("relativeURL(%s, %s) = %s, want %s", in[0], in[1], got, want)
		}
		base, _ := url.Parse("http://host/prefix" + in[0])
		if got := base.ResolveReference(&url.URL{Path: want}).Path; got != "/prefix"+in[1] {
			t.Errorf("%s from %s resolves to %s", want, in[0], got)
		}
	}
}
