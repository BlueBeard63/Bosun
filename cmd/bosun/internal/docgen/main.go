// Command docgen prepares the embedded docs content from the hand-written
// Markdown in docs/: an ASCII-normalized copy of each Markdown source and a JSON
// search index covering page title, headings, and body text. The docs site
// renders the Markdown to HTML at request time (see internal/docsite), so no HTML
// is generated or committed here. Run via `go generate ./...` in cmd/bosun; the
// output under internal/docsite/content is embedded into the binary.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"

	"github.com/amberstack/bosun/cmd/bosun/internal/docrender"
)

// Heading is one heading in a page, for the table of contents and search.
type Heading struct {
	Level  int    `json:"level"`
	Text   string `json:"text"`
	Anchor string `json:"anchor"`
}

// IndexEntry is one page in the search index.
type IndexEntry struct {
	Slug     string    `json:"slug"`
	Title    string    `json:"title"`
	Section  string    `json:"section"`
	Group    string    `json:"group,omitempty"`
	Order    int       `json:"order"`
	Headings []Heading `json:"headings"`
	Text     string    `json:"text"` // plain-text body for full-text search
}

func main() {
	in := flag.String("in", "../../docs", "docs markdown directory")
	out := flag.String("out", "./internal/docsite/content", "output content directory")
	flag.Parse()

	if err := run(*in, *out); err != nil {
		fmt.Fprintln(os.Stderr, "docgen:", err)
		os.Exit(1)
	}
}

func run(inDir, outDir string) error {
	md := docrender.New()

	entries, err := os.ReadDir(inDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	var index []IndexEntry
	for _, e := range entries {
		if e.IsDir() {
			if e.Name() == "assets" {
				if err := copyTree(filepath.Join(inDir, "assets"), filepath.Join(outDir, "docassets")); err != nil {
					return err
				}
			}
			continue
		}
		if !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(inDir, e.Name()))
		if err != nil {
			return err
		}
		src := []byte(docrender.ASCIINormalize(string(raw)))
		slug := slugForFile(e.Name())

		doc := md.Parser().Parse(text.NewReader(src))
		title, headings := scan(doc, src)
		if title == "" {
			title = titleize(slug)
		}

		// Write the ASCII-normalized Markdown source. The site renders it to HTML
		// on request; the MCP/AI reader serves it verbatim.
		if err := os.WriteFile(filepath.Join(outDir, slug+".md"), src, 0o644); err != nil {
			return err
		}

		// Render once here only to derive the plain-text body for the search index.
		var htmlBuf strings.Builder
		if err := md.Renderer().Render(&htmlBuf, src, doc); err != nil {
			return err
		}

		section, group, order := placeFor(slug)
		index = append(index, IndexEntry{
			Slug:     slug,
			Title:    title,
			Section:  section,
			Group:    group,
			Order:    order,
			Headings: headings,
			Text:     plainText(htmlBuf.String()),
		})
	}

	sort.SliceStable(index, func(i, j int) bool {
		if index[i].Order != index[j].Order {
			return index[i].Order < index[j].Order
		}
		return index[i].Title < index[j].Title
	})

	f, err := os.Create(filepath.Join(outDir, "index.json"))
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(index); err != nil {
		return err
	}
	fmt.Printf("docgen: prepared %d pages -> %s\n", len(index), outDir)
	return nil
}

// scan extracts the first H1 as the page title and every heading (with its
// auto-generated anchor id) for the table of contents / search.
func scan(doc ast.Node, src []byte) (title string, headings []Heading) {
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		h, ok := n.(*ast.Heading)
		if !ok {
			return ast.WalkContinue, nil
		}
		txt := nodeText(h, src)
		if h.Level == 1 && title == "" {
			title = txt
		}
		anchor := ""
		if id, ok := h.AttributeString("id"); ok {
			anchor = fmt.Sprint(attrString(id))
		}
		headings = append(headings, Heading{Level: h.Level, Text: txt, Anchor: anchor})
		return ast.WalkContinue, nil
	})
	return title, headings
}

func attrString(v any) string {
	switch t := v.(type) {
	case []byte:
		return string(t)
	case string:
		return t
	default:
		return fmt.Sprint(v)
	}
}

func nodeText(n ast.Node, src []byte) string {
	var b strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch t := c.(type) {
		case *ast.Text:
			b.Write(t.Segment.Value(src))
		case *ast.String:
			b.Write(t.Value)
		default:
			b.WriteString(nodeText(c, src))
		}
	}
	return b.String()
}

var tagRE = regexp.MustCompile(`<[^>]+>`)
var wsRE = regexp.MustCompile(`\s+`)

// plainText strips HTML tags and collapses whitespace for full-text search.
func plainText(h string) string {
	s := tagRE.ReplaceAllString(h, " ")
	s = html.UnescapeString(s)
	s = wsRE.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func slugForFile(name string) string {
	base := strings.TrimSuffix(name, ".md")
	if strings.EqualFold(base, "README") {
		return "index"
	}
	return base
}

func titleize(slug string) string {
	parts := strings.FieldsFunc(slug, func(r rune) bool { return r == '-' || r == '_' })
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}

// navGroup is a run of pages inside a section. An empty name means the pages
// sit directly under the section; a non-empty name renders them as a
// collapsible sub-group (mirrors the "SubCategory" component in the docs
// site design).
type navGroup struct {
	name  string
	slugs []string
}

// sections define the sidebar grouping and reading order. Unknown pages fall
// into "More" after everything else.
var sections = []struct {
	name   string
	groups []navGroup
}{
	{"Getting started", []navGroup{{"", []string{"index", "getting-started", "api-overview"}}}},
	{"Core concepts", []navGroup{
		{"", []string{"controllers", "services", "service-options"}},
		{"Middleware", []string{"middleware", "auth-and-permissions"}},
		{"", []string{"typed-handlers", "errors", "convert"}},
	}},
	{"Routing", []navGroup{{"", []string{"routing-groups", "routing-internals"}}}},
	{"Inputs", []navGroup{{"", []string{"forms", "files"}}}},
	{"Data layer", []navGroup{
		{"", []string{"repo"}},
		{"Databases", []string{"database-gorm", "database-sqlc"}},
	}},
	{"Messaging", []navGroup{
		{"Events", []string{"events", "event-backends", "event-rpc"}},
		{"", []string{"webhooks", "outbox"}},
	}},
	{"Platform", []navGroup{{"", []string{"storage", "secrets-infisical", "health", "manifest", "multitenancy", "tracing"}}}},
	{"CLI & tooling", []navGroup{{"", []string{"cli", "mcp", "client-gen", "microservices"}}}},
	{"Operations", []navGroup{{"", []string{"config", "registry", "openapi", "testing"}}}},
}

type place struct {
	section string
	group   string
	order   int
}

var placeIndex = func() map[string]place {
	m := map[string]place{}
	order := 0
	for _, sec := range sections {
		for _, g := range sec.groups {
			for _, slug := range g.slugs {
				m[slug] = place{section: sec.name, group: g.name, order: order}
				order++
			}
		}
	}
	return m
}()

func placeFor(slug string) (section, group string, order int) {
	if p, ok := placeIndex[slug]; ok {
		return p.section, p.group, p.order
	}
	return "More", "", 10000
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
