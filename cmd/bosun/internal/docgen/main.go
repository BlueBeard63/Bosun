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

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
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
		goldmark.WithExtensions(extension.GFM),
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
		src, err := os.ReadFile(filepath.Join(inDir, e.Name()))
		if err != nil {
			return err
		}
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

		index = append(index, IndexEntry{
			Slug:     slug,
			Title:    title,
			Order:    orderFor(slug),
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

// navOrder pins the reading order of the most important pages; everything else
// sorts alphabetically after them (order 1000).
var navOrder = map[string]int{
	"index":             0,
	"getting-started":   1,
	"api-overview":      2,
	"controllers":       3,
	"services":          4,
	"service-options":   5,
	"middleware":        6,
	"typed-handlers":    7,
	"routing-groups":    8,
	"routing-internals": 9,
	"errors":            10,
	"convert":           11,
	"forms":             12,
	"files":             13,
	"repo":              14,
	"database-gorm":     15,
	"database-sqlc":     16,
	"config":            17,
	"registry":          18,
	"openapi":           19,
	"health":            20,
	"events":            21,
	"storage":           22,
	"webhooks-outbox":   23,
	"tracing":           24,
	"multitenancy":      25,
	"manifest":          26,
	"secrets-infisical": 27,
	"cli":               28,
	"mcp":               29,
	"microservices":     30,
	"client-gen":        31,
}

func orderFor(slug string) int {
	if o, ok := navOrder[slug]; ok {
		return o
	}
	return 1000
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
