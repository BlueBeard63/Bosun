package openapi

import (
	"html/template"
	"net/http"
	"strings"

	"github.com/bluebeard63/bosun"
)

// DefaultAssetsURL is the pinned Swagger UI build the docs page loads when
// DocsOptions.AssetsURL is empty.
const DefaultAssetsURL = "https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14"

// DocsOptions configures the interactive docs page SpecController serves
// next to the spec. The default serves Swagger UI at /docs. Override it
// before Start:
//
//	registry.RegisterInstance[*openapi.DocsOptions](app.Reg, &openapi.DocsOptions{
//	    Path:      "/reference",
//	    AssetsURL: "/static/swagger-ui", // self-hosted swagger-ui-dist
//	})
//
// The page and its script are raw routes under the controller's prefix
// (OverridePrefix moves them with the spec), so they are not in the spec.
// The page loads the spec by a URL relative to itself, so it also works
// behind a proxy that strips a path prefix.
type DocsOptions struct {
	// Path of the page, e.g. "/docs". Empty turns the page off; the spec
	// is still served. A trailing slash is kept: "/docs/" serves only
	// "/docs/".
	Path string

	// Title of the page. Empty means Options.Title, or "bosun API".
	Title string

	// AssetsURL is the base URL of a swagger-ui-dist build: the page loads
	// AssetsURL/swagger-ui.css and AssetsURL/swagger-ui-bundle.js. Point it
	// at self-hosted files for offline or strict-CSP deployments. Empty
	// means DefaultAssetsURL.
	AssetsURL string
}

var _ = bosun.Default[*DocsOptions](func() *DocsOptions {
	return &DocsOptions{Path: "/docs"}
})

// specPath is where Routes mounts the spec, relative to the prefix.
const specPath = "/openapi.json"

// docsRoutes mounts the docs page and its script when DocsOptions.Path is
// set. Raw routes, so neither appears in the spec.
func (c *SpecController) docsRoutes(r *bosun.Router) {
	if c.docs == nil || c.docs.Path == "" {
		return
	}
	page := c.docs.Path
	if !strings.HasPrefix(page, "/") {
		page = "/" + page
	}
	script := strings.TrimSuffix(page, "/") + "/init.js"
	pattern := page
	if strings.HasSuffix(pattern, "/") {
		pattern += "{$}" // exact match, and survives joining onto a prefix
	}
	c.docsPage = docsPage{
		Title:  c.docsTitle(),
		Assets: strings.TrimSuffix(c.docsAssets(), "/"),
		Spec:   relativeURL(page, specPath),
		Script: relativeURL(page, script),
	}
	r.Get(pattern, c.Docs)
	r.Get(script, c.DocsScript)
}

func (c *SpecController) docsTitle() string {
	if c.docs.Title != "" {
		return c.docs.Title
	}
	return c.opts.info()["title"].(string)
}

func (c *SpecController) docsAssets() string {
	if c.docs.AssetsURL != "" {
		return c.docs.AssetsURL
	}
	return DefaultAssetsURL
}

// relativeURL returns a URL that, resolved against a page served at from,
// points at to. Both are paths under the same prefix, so the result holds
// wherever that prefix is mounted.
func relativeURL(from, to string) string {
	dir := from[:strings.LastIndex(from, "/")+1]
	if depth := strings.Count(dir, "/") - 1; depth > 0 {
		return strings.Repeat("../", depth) + strings.TrimPrefix(to, "/")
	}
	return "./" + strings.TrimPrefix(to, "/")
}

// docsPage is the data the docs page template renders.
type docsPage struct {
	Title  string
	Assets string // swagger-ui-dist base URL, no trailing slash
	Spec   string // spec URL relative to the page
	Script string // init script URL relative to the page
}

// The page has no inline script, so a CSP that allows only the page's own
// origin and the assets origin is enough.
var docsTemplate = template.Must(template.New("docs").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<link rel="stylesheet" href="{{.Assets}}/swagger-ui.css">
</head>
<body>
<div id="swagger-ui" data-spec-url="{{.Spec}}"></div>
<script src="{{.Assets}}/swagger-ui-bundle.js"></script>
<script src="{{.Script}}"></script>
</body>
</html>
`))

const docsScript = `window.addEventListener("load", function () {
  var el = document.getElementById("swagger-ui");
  window.ui = SwaggerUIBundle({
    url: new URL(el.getAttribute("data-spec-url"), document.baseURI).href,
    dom_id: "#swagger-ui",
    deepLinking: true
  });
});
`

// Docs serves the interactive docs page.
func (c *SpecController) Docs(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = docsTemplate.Execute(w, c.docsPage)
}

// DocsScript serves the script that starts Swagger UI on the docs page.
func (c *SpecController) DocsScript(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	_, _ = w.Write([]byte(docsScript))
}
