// Package docsite serves the embedded Bosun documentation site and provides the
// search + page-fetch primitives the MCP server reuses. The Markdown sources and
// search index are embedded (go:embed) from internal/docsite/content, prepared by
// cmd/bosun/internal/docgen. Pages are rendered to HTML at request time (and
// cached), so the binary is fully self-contained and works offline while shipping
// only the raw Markdown, never generated HTML.
package docsite

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/yuin/goldmark"

	"github.com/bluebeard63/bosun/cmd/bosun/internal/docrender"
)

//go:embed content
var contentFS embed.FS

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
	Section  string    `json:"section"`
	Group    string    `json:"group,omitempty"`
	Order    int       `json:"order"`
	Headings []Heading `json:"headings"`
	Text     string    `json:"text"`
}

// Site is the loaded documentation set.
type Site struct {
	entries []IndexEntry
	valid   map[string]bool // known slugs, guards against path traversal
	content fs.FS           // *.md, docassets/, index.json
	md      goldmark.Markdown

	mu    sync.RWMutex
	cache map[string]string // slug -> rendered HTML

	// Version labels the docs build (shown in the header pill); optional.
	Version string
}

// Load reads the embedded index and returns a ready Site.
func Load() (*Site, error) {
	content, err := fs.Sub(contentFS, "content")
	if err != nil {
		return nil, err
	}
	raw, err := fs.ReadFile(content, "index.json")
	if err != nil {
		return nil, err
	}
	var entries []IndexEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	valid := make(map[string]bool, len(entries))
	for _, e := range entries {
		valid[e.Slug] = true
	}
	return &Site{
		entries: entries,
		valid:   valid,
		content: content,
		md:      docrender.New(),
		cache:   map[string]string{},
	}, nil
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

// Page returns the rendered HTML fragment for a slug, rendering the Markdown on
// first request and caching the result.
func (s *Site) Page(slug string) (string, bool) {
	if !s.valid[slug] {
		return "", false
	}
	s.mu.RLock()
	if h, ok := s.cache[slug]; ok {
		s.mu.RUnlock()
		return h, true
	}
	s.mu.RUnlock()

	src, err := fs.ReadFile(s.content, slug+".md")
	if err != nil {
		return "", false
	}
	var buf strings.Builder
	if err := s.md.Convert(src, &buf); err != nil {
		return "", false
	}
	out := buf.String()

	s.mu.Lock()
	s.cache[slug] = out
	s.mu.Unlock()
	return out, true
}

// Markdown returns the raw Markdown source for a slug — the AI-friendly form the
// MCP get_doc tool returns.
func (s *Site) Markdown(slug string) (string, bool) {
	if !s.valid[slug] {
		return "", false
	}
	b, err := fs.ReadFile(s.content, slug+".md")
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
			return text[:160] + "..."
		}
		return text
	}
	start := max(0, idx-60)
	end := min(len(text), idx+100)
	prefix, suffix := "", ""
	if start > 0 {
		prefix = "..."
	}
	if end < len(text) {
		suffix = "..."
	}
	return prefix + strings.TrimSpace(text[start:end]) + suffix
}

// Handler serves the documentation website: the SPA shell at /, static assets,
// page fragments rendered on demand, doc images, and the search index JSON.
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
	mux.Handle("/docassets/", http.FileServer(http.FS(s.content)))

	// Page fragments are rendered from Markdown on request (cached in Page).
	mux.HandleFunc("/pages/", func(w http.ResponseWriter, r *http.Request) {
		slug := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/pages/"), ".html")
		htmlFrag, ok := s.Page(slug)
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(htmlFrag))
	})

	mux.HandleFunc("/search-index.json", func(w http.ResponseWriter, r *http.Request) {
		b, err := fs.ReadFile(s.content, "index.json")
		if err != nil {
			http.Error(w, "no index", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	})

	mux.HandleFunc("/meta.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"version": s.Version})
	})

	return mux
}
