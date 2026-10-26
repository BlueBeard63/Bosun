package docrender

import (
	"bytes"
	"strings"
	"testing"
)

func render(t *testing.T, src string) string {
	t.Helper()
	var b bytes.Buffer
	if err := New().Convert([]byte(src), &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestHeadingIDs(t *testing.T) {
	out := render(t, "# Getting started\n\n## Install the CLI\n")
	for _, want := range []string{`<h1 id="getting-started">`, `<h2 id="install-the-cli">`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in:\n%s", want, out)
		}
	}
}

func TestGFM(t *testing.T) {
	out := render(t, "| a | b |\n|---|---|\n| 1 | 2 |\n\n~~gone~~\n\n- [x] done\n\nhttps://example.com\n")
	for _, want := range []string{"<table>", "<del>gone</del>", `type="checkbox"`, `<a href="https://example.com">`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in:\n%s", want, out)
		}
	}
}

// Code is highlighted with CSS classes, not inline colors, so the site's
// light/dark tokens style it.
func TestHighlightUsesClasses(t *testing.T) {
	out := render(t, "```go\nfunc main() {}\n```\n")
	if !strings.Contains(out, `class="chroma"`) {
		t.Errorf("code block not highlighted with classes:\n%s", out)
	}
	if strings.Contains(out, "style=") {
		t.Errorf("highlighted code has inline styles:\n%s", out)
	}
}

// The docs are trusted and embed inline SVG, so raw HTML passes through.
func TestRawHTMLPassesThrough(t *testing.T) {
	out := render(t, "<svg viewBox=\"0 0 1 1\"><rect/></svg>\n")
	if !strings.Contains(out, `<svg viewBox="0 0 1 1"><rect/></svg>`) {
		t.Errorf("raw HTML was escaped or dropped:\n%s", out)
	}
}

func TestASCIINormalize(t *testing.T) {
	in := "a — b – c ― d… e → f ← g ⇒ h “q” ‘s’ • i · j k"
	want := `a - b - c - d... e -> f <- g => h "q" 's' - i - j k`
	if got := ASCIINormalize(in); got != want {
		t.Errorf("ASCIINormalize = %q, want %q", got, want)
	}
	if plain := "plain `code` -> stays"; ASCIINormalize(plain) != plain {
		t.Errorf("ASCII input changed: %q", ASCIINormalize(plain))
	}
}
