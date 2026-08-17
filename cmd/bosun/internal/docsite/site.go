// Package docsite serves the embedded Bosun documentation site and provides the
// search + page-fetch primitives the MCP server reuses. All content is embedded
// (go:embed) at build time by cmd/bosun/internal/docgen, so the binary is fully
// self-contained and works offline.
package docsite

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"sort"
	"strings"
)

//go:embed dist
var distFS embed.FS

//go:embed assets
var assetsFS embed.FS

// Heading mirrors docgen.Heading.
type Heading struct {
	Level  int    `json:"level"`
	Text   string `json:"text"`
	Anchor string `json:"anchor"`
}

// IndexEntry mirrors docgen.IndexEntry (search fields for one page).
type IndexEntry struct {
	Slug     string    `json:"slug"`
	Title    string    `json:"title"`
	Order    int       `json:"order"`
	Headings []Heading `json:"headings"`
	Text     string    `json:"text"`
}

// Site is the loaded documentation set.
type Site struct {
	entries []IndexEntry
	dist    fs.FS // pages/, md/, docassets/, index.json
}

// Load reads the embedded index and returns a ready Site.
func Load() (*Site, error) {
	dist, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil, err
	}
	raw, err := fs.ReadFile(dist, "index.json")
	if err != nil {
		return nil, err
	}
	var entries []IndexEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	return &Site{entries: entries, dist: dist}, nil
}

// Entries returns the page index in reading order.
func (s *Site) Entries() []IndexEntry { return s.entries }

// Slugs returns every page slug in reading order.
func (s *Site) Slugs() []string {
	out := make([]string, len(s.entries))
	for i, e := range s.entries {
		out[i] = e.Slug
	}
	return out
}

// Title returns a page's title, or "" if the slug is unknown.
func (s *Site) Title(slug string) string {
	for _, e := range s.entries {
		if e.Slug == slug {
			return e.Title
		}
	}
	return ""
}

// Page returns the rendered HTML fragment for a slug.
func (s *Site) Page(slug string) (string, bool) {
	b, err := fs.ReadFile(s.dist, "pages/"+slug+".html")
	if err != nil {
		return "", false
	}
	return string(b), true
}

// Markdown returns the raw Markdown source for a slug — the AI-friendly form the
// MCP get_doc tool returns.
func (s *Site) Markdown(slug string) (string, bool) {
	b, err := fs.ReadFile(s.dist, "md/"+slug+".md")
	if err != nil {
		return "", false
	}
	return string(b), true
}

// Result is a scored search hit.
type Result struct {
	Slug    string `json:"slug"`
	Title   string `json:"title"`
	Score   int    `json:"score"`
	Snippet string `json:"snippet"`
}

// Search ranks pages against a free-text query over title (weight 5), headings
// (3), and body text (1). Used by the MCP search_docs tool; the web UI runs the
// same ranking client-side over /search-index.json.
func (s *Site) Search(query string) []Result {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return nil
	}
	var out []Result
	for _, e := range s.entries {
		title := strings.ToLower(e.Title)
		var headings strings.Builder
		for _, h := range e.Headings {
			headings.WriteString(strings.ToLower(h.Text))
			headings.WriteByte(' ')
		}
		body := strings.ToLower(e.Text)

		score := 0
		for _, t := range terms {
			score += 5 * strings.Count(title, t)
			score += 3 * strings.Count(headings.String(), t)
			score += strings.Count(body, t)
		}
		if score == 0 {
			continue
		}
		out = append(out, Result{Slug: e.Slug, Title: e.Title, Score: score, Snippet: snippet(e.Text, terms)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// snippet returns a short window of body text around the first matched term.
func snippet(text string, terms []string) string {
	lower := strings.ToLower(text)
	idx := -1
	for _, t := range terms {
		if i := strings.Index(lower, t); i >= 0 && (idx < 0 || i < idx) {
			idx = i
		}
	}
	if idx < 0 {
		if len(text) > 160 {
			return text[:160] + "…"
		}
		return text
	}
	start := max(0, idx-60)
	end := min(len(text), idx+100)
	prefix, suffix := "", ""
	if start > 0 {
		prefix = "…"
	}
	if end < len(text) {
		suffix = "…"
	}
	return prefix + strings.TrimSpace(text[start:end]) + suffix
}

// Handler serves the documentation website: the SPA shell at /, static assets,
// rendered page fragments, doc images, and the search index JSON.
func (s *Site) Handler() http.Handler {
	mux := http.NewServeMux()

	shell, _ := assetsFS.ReadFile("assets/index.html")
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(shell)
	})

	mux.Handle("/assets/", http.FileServer(http.FS(assetsFS)))
	mux.Handle("/pages/", http.FileServer(http.FS(s.dist)))
	mux.Handle("/docassets/", http.FileServer(http.FS(s.dist)))

	mux.HandleFunc("/search-index.json", func(w http.ResponseWriter, r *http.Request) {
		b, err := fs.ReadFile(s.dist, "index.json")
		if err != nil {
			http.Error(w, "no index", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	})

	return mux
}
