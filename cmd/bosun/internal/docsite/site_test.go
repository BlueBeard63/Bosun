package docsite

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func load(t *testing.T) *Site {
	t.Helper()
	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(s.Entries()) == 0 {
		t.Fatal("no doc entries embedded — run `go generate ./cmd/bosun/...`")
	}
	return s
}

func TestLoadAndPages(t *testing.T) {
	s := load(t)
	// A known page from the existing docs.
	html, ok := s.Page("getting-started")
	if !ok {
		t.Fatal("getting-started page missing")
	}
	if !strings.Contains(html, "<h1") {
		t.Fatal("rendered page has no h1")
	}
	md, ok := s.Markdown("getting-started")
	if !ok || !strings.Contains(md, "#") {
		t.Fatal("markdown source missing or not markdown")
	}
	if s.Title("getting-started") == "" {
		t.Fatal("title missing")
	}
}

func TestSearchRanksTitleFirst(t *testing.T) {
	s := load(t)
	hits := s.Search("middleware")
	if len(hits) == 0 {
		t.Fatal("no hits for 'middleware'")
	}
	if hits[0].Slug != "middleware" {
		t.Fatalf("expected middleware page ranked first, got %q", hits[0].Slug)
	}
	if hits[0].Snippet == "" {
		t.Fatal("snippet empty")
	}
}

func TestHandlerRoutes(t *testing.T) {
	s := load(t)
	s.Version = "v9.9.9"
	h := s.Handler()

	cases := []struct {
		path, contains string
	}{
		{"/", "<title>Bosun docs</title>"},
		{"/search-index.json", "\"slug\""},
		{"/pages/getting-started.html", "<h1"},
		{"/assets/style.css", "--accent"},
		{"/assets/app.js", "openPalette"},
		{"/meta.json", "v9.9.9"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", c.path, nil))
		if rec.Code != 200 {
			t.Fatalf("%s -> %d", c.path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), c.contains) {
			t.Fatalf("%s missing %q", c.path, c.contains)
		}
	}
}
