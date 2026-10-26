package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSlugAndTitle(t *testing.T) {
	for name, slug := range map[string]string{
		"README.md":       "index",
		"readme.md":       "index",
		"event-rpc.md":    "event-rpc",
		"database_sql.md": "database_sql",
	} {
		if got := slugForFile(name); got != slug {
			t.Errorf("slugForFile(%q) = %q, want %q", name, got, slug)
		}
	}
	for slug, title := range map[string]string{
		"event-rpc":      "Event Rpc",
		"database_gorm":  "Database Gorm",
		"a--b":           "A B",
		"already-Upper":  "Already Upper",
		"single":         "Single",
		"trailing-dash-": "Trailing Dash",
	} {
		if got := titleize(slug); got != title {
			t.Errorf("titleize(%q) = %q, want %q", slug, got, title)
		}
	}
}

func TestPlaceFor(t *testing.T) {
	sec, group, order := placeFor("index")
	if sec != "Getting started" || group != "" || order != 0 {
		t.Errorf("placeFor(index) = %q %q %d", sec, group, order)
	}
	sec, group, _ = placeFor("middleware")
	if sec != "Core concepts" || group != "Middleware" {
		t.Errorf("placeFor(middleware) = %q %q", sec, group)
	}
	_, _, before := placeFor("controllers")
	_, _, after := placeFor("validation")
	if before >= after {
		t.Errorf("controllers (%d) should sort before validation (%d)", before, after)
	}
	sec, group, order = placeFor("not-in-the-nav")
	if sec != "More" || group != "" || order != 10000 {
		t.Errorf("placeFor(unknown) = %q %q %d", sec, group, order)
	}
}

// Every slug appears in the nav at most once; a duplicate would silently
// take the later position.
func TestSectionsHaveNoDuplicateSlugs(t *testing.T) {
	seen := map[string]bool{}
	for _, sec := range sections {
		for _, g := range sec.groups {
			for _, slug := range g.slugs {
				if seen[slug] {
					t.Errorf("slug %q listed twice", slug)
				}
				seen[slug] = true
			}
		}
	}
}

func TestPlainText(t *testing.T) {
	got := plainText("<h1 id=\"x\">Title</h1>\n<p>Fish &amp; chips,\n  <code>go   test</code></p>")
	if want := "Title Fish & chips, go test"; got != want {
		t.Errorf("plainText = %q, want %q", got, want)
	}
}

// TestRunIndex checks the generated index: titles from the first H1 (or the
// file name), headings with anchors, ASCII-normalized source and text, and
// pages ordered by the nav.
func TestRunIndex(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	for name, body := range map[string]string{
		"README.md":      "# Bosun — home\n\nIntro.\n\n## First steps\n",
		"middleware.md":  "# Middleware\n\n### Ordering\n\nRuns before → after.\n",
		"zz-untitled.md": "No heading here.\n",
		"notes.txt":      "ignored",
	} {
		if err := os.WriteFile(filepath.Join(in, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := run(in, out); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(filepath.Join(out, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var index []IndexEntry
	if err := json.Unmarshal(b, &index); err != nil {
		t.Fatal(err)
	}
	if len(index) != 3 {
		t.Fatalf("got %d pages, want 3: %+v", len(index), index)
	}
	home, mw, untitled := index[0], index[1], index[2]

	if home.Slug != "index" || home.Title != "Bosun - home" || home.Section != "Getting started" {
		t.Errorf("home = %+v", home)
	}
	if len(home.Headings) != 2 || home.Headings[1] != (Heading{Level: 2, Text: "First steps", Anchor: "first-steps"}) {
		t.Errorf("home headings = %+v", home.Headings)
	}
	if home.Text != "Bosun - home Intro. First steps" {
		t.Errorf("home text = %q", home.Text)
	}

	if mw.Slug != "middleware" || mw.Group != "Middleware" || mw.Text != "Middleware Ordering Runs before -> after." {
		t.Errorf("middleware = %+v", mw)
	}

	if untitled.Slug != "zz-untitled" || untitled.Title != "Zz Untitled" || untitled.Section != "More" || len(untitled.Headings) != 0 {
		t.Errorf("untitled = %+v", untitled)
	}

	src, err := os.ReadFile(filepath.Join(out, "index.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(src) != "# Bosun - home\n\nIntro.\n\n## First steps\n" {
		t.Errorf("index.md not ASCII-normalized: %q", src)
	}
	if _, err := os.Stat(filepath.Join(out, "notes.md")); !os.IsNotExist(err) {
		t.Errorf("non-Markdown file was copied (err = %v)", err)
	}
}
