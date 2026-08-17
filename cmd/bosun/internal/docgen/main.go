// Command docgen renders the hand-written Markdown in docs/ into the embedded
// docs site: an HTML fragment per page, a copy of each Markdown source (for the
// MCP/AI reader), and a JSON search index covering page title, headings, and
// body text. It is a build-time tool with the only goldmark dependency in the
// repo — run via `go generate ./...` in cmd/bosun; the rendered output under
// internal/docsite/dist is embedded into the binary.
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

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/parser"
	gmhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
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
	out := flag.String("out", "./internal/docsite/dist", "output dist directory")
	flag.Parse()

	if err := run(*in, *out); err != nil {
		fmt.Fprintln(os.Stderr, "docgen:", err)
		os.Exit(1)
	}
}

func run(inDir, outDir string) error {
	md := goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
			// Build-time syntax highlighting. Class-based output (no inline
			// colors) so light/dark theming is driven by our CSS token vars.
			highlighting.NewHighlighting(
				highlighting.WithFormatOptions(chromahtml.WithClasses(true)),
			),
		),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
		goldmark.WithRendererOptions(gmhtml.WithUnsafe()), // trusted, in-repo docs
	)

	entries, err := os.ReadDir(inDir)
	if err != nil {
		return err
	}

	pagesDir := filepath.Join(outDir, "pages")
	mdDir := filepath.Join(outDir, "md")
	for _, d := range []string{pagesDir, mdDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
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
		src := []byte(asciiNormalize(string(raw)))
		slug := slugForFile(e.Name())

		doc := md.Parser().Parse(text.NewReader(src))
		title, headings := scan(doc, src)
		if title == "" {
			title = titleize(slug)
		}

		var htmlBuf strings.Builder
		if err := md.Renderer().Render(&htmlBuf, src, doc); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(pagesDir, slug+".html"), []byte(htmlBuf.String()), 0o644); err != nil {
			return err
		}
		// Copy the raw markdown for the MCP/AI reader.
		if err := os.WriteFile(filepath.Join(mdDir, slug+".md"), src, 0o644); err != nil {
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
	fmt.Printf("docgen: rendered %d pages -> %s\n", len(index), outDir)
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

// asciiNormalize replaces the non-ASCII typographic characters that creep into
// prose (em/en dashes, smart quotes, ellipsis, arrows, non-breaking spaces)
// with plain ASCII equivalents. Bosun's code samples are Go/shell and never
// contain these glyphs, so normalizing the whole source is safe and keeps every
// rendered title, heading, and paragraph ASCII-clean.
var asciiReplacer = strings.NewReplacer(
	"—", "-", // em dash
	"–", "-", // en dash
	"―", "-", // horizontal bar
	"…", "...", // ellipsis
	"→", "->", // rightwards arrow
	"←", "<-", // leftwards arrow
	"⇒", "=>", // rightwards double arrow
	"“", "\"", // left double quote
	"”", "\"", // right double quote
	"‘", "'", // left single quote
	"’", "'", // right single quote
	"•", "-", // bullet
	"·", "-", // middle dot
	" ", " ", // non-breaking space
)

func asciiNormalize(s string) string { return asciiReplacer.Replace(s) }

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
