// Package docrender centralizes Markdown rendering so the build-time docgen tool
// and the runtime docs site produce identical HTML: the same GFM extensions, the
// same class-based Chroma highlight markup (themed via the site's CSS vars), the
// same auto heading ids, and the same ASCII normalization of prose.
package docrender

import (
	"strings"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/parser"
	gmhtml "github.com/yuin/goldmark/renderer/html"
)

// New returns the goldmark instance shared by docgen and the docs site.
//
// Highlighting is class-based (no inline colors) so light/dark theming is driven
// by the site's CSS token vars, and raw HTML passes through unescaped because the
// docs are trusted, in-repo content that embeds inline SVG diagrams.
func New() goldmark.Markdown {
	return goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
			highlighting.NewHighlighting(
				highlighting.WithFormatOptions(chromahtml.WithClasses(true)),
			),
		),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
		goldmark.WithRendererOptions(gmhtml.WithUnsafe()),
	)
}

// asciiReplacer maps the non-ASCII typographic characters that creep into prose
// (em/en dashes, smart quotes, ellipsis, arrows, non-breaking spaces) to plain
// ASCII. Bosun's code samples are Go/shell and never contain these glyphs, so
// normalizing the whole source is safe and keeps every rendered title, heading,
// and paragraph ASCII-clean.
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

// ASCIINormalize replaces non-ASCII typographic characters with ASCII equivalents.
func ASCIINormalize(s string) string { return asciiReplacer.Replace(s) }
