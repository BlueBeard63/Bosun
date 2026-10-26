package openapi

import (
	"context"
	"reflect"
	"testing"

	"github.com/bluebeard63/bosun"
)

// --- fixtures: doc comments as bosun gen registry would register them ---

type DocNote struct {
	ID    int    `path:"id"`
	Title string `json:"title"`
	Body  string `json:"body"`
	DocMeta
}

type DocMeta struct {
	Author string `json:"author"`
}

type docPage[T any] struct{ Items []T }

type DocController struct{}

var _ = bosun.Controller[DocController]("/doc")

func (c *DocController) Routes(r *bosun.Router) {
	bosun.Put(r, "/notes/{id}", c.Update)
	bosun.Get(r, "/notes/{id}", c.Get, bosun.Doc("Fetch a note", ""))
	bosun.Delete(r, "/notes/{id}", c.Delete, bosun.Doc("Remove a note", "Idempotent."))
	bosun.Get(r, "/plain", c.Plain)
}

func (c *DocController) Update(ctx context.Context, req *bosun.Req[DocNote]) (DocNote, error) {
	return req.Body, nil
}

func (c *DocController) Get(ctx context.Context, req *bosun.Req[DocNote]) (DocNote, error) {
	return req.Body, nil
}

func (c *DocController) Delete(ctx context.Context, req *bosun.Req[DocNote]) (struct{}, error) {
	return struct{}{}, nil
}

func (c *DocController) Plain(ctx context.Context, _ *bosun.Req[struct{}]) (ShapeIn, error) {
	return ShapeIn{}, nil
}

const docPkg = "github.com/bluebeard63/bosun/openapi"

var _ = bosun.RegisterDocComments(map[string]string{
	docPkg + ".(*DocController).Update": "Update replaces a note's title and body.\nThe author is kept.\n\nReturns the saved note.\n",
	docPkg + ".(*DocController).Get":    "Get returns one note, e.g. for editing. It is cached.\n",
	docPkg + ".DocNote":                 "DocNote is a saved note.\n",
	docPkg + ".DocNote.Title":           "Title is shown in lists.\n",
	docPkg + ".DocNote.ID":              "ID of the note.",
	docPkg + ".DocMeta.Author":          "Author wrote the note.",
})

func TestSpecOperationDocs(t *testing.T) {
	app := bosun.New()
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	spec := fetchSpec(t, app)
	assertValidSpec(t, spec)

	cases := []struct {
		method, summary, description string
	}{
		// from the comment: first sentence, then the rest
		{"put", "Update replaces a note's title and body.", "The author is kept.\n\nReturns the saved note."},
		// bosun.Doc's summary wins; the comment's description stays
		{"get", "Fetch a note", "It is cached."},
		// bosun.Doc without a comment
		{"delete", "Remove a note", "Idempotent."},
	}
	for _, c := range cases {
		op := opOf(t, spec, "/doc/notes/{id}", c.method)
		if op["summary"] != c.summary || op["description"] != c.description {
			t.Errorf("%s: summary %q, description %q; want %q, %q",
				c.method, op["summary"], op["description"], c.summary, c.description)
		}
	}

	// No comment and no Doc: no empty keys.
	op := opOf(t, spec, "/doc/plain", "get")
	for _, k := range []string{"summary", "description"} {
		if _, ok := op[k]; ok {
			t.Errorf("undocumented operation has %s", k)
		}
	}

	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
	note := schemas["OpenapiDocNote"].(map[string]any)
	if note["description"] != "DocNote is a saved note." {
		t.Fatalf("type description = %v", note["description"])
	}
	props := note["properties"].(map[string]any)
	if d := props["title"].(map[string]any)["description"]; d != "Title is shown in lists." {
		t.Fatalf("field description = %v", d)
	}
	if d := props["author"].(map[string]any)["description"]; d != "Author wrote the note." {
		t.Fatalf("promoted field description = %v", d)
	}
	if _, ok := props["body"].(map[string]any)["description"]; ok {
		t.Fatal("uncommented field has a description")
	}
	if _, ok := schemas["OpenapiShapeIn"].(map[string]any)["description"]; ok {
		t.Fatal("uncommented type has a description")
	}
	params := opOf(t, spec, "/doc/notes/{id}", "put")["parameters"].([]any)
	if d := params[0].(map[string]any)["description"]; d != "ID of the note." {
		t.Fatalf("path parameter description = %v", d)
	}
}

func TestSplitDoc(t *testing.T) {
	cases := []struct{ in, first, rest string }{
		{"", "", ""},
		{"Get returns a note.", "Get returns a note.", ""},
		{"Get returns a note. It is cached.", "Get returns a note.", "It is cached."},
		{"Get returns\na note.\nIt is cached.", "Get returns a note.", "It is cached."},
		{"Uses e.g. a cache. Then more.", "Uses e.g. a cache.", "Then more."},
		{"No period here\n\nSecond paragraph.", "No period here", "Second paragraph."},
		{"Version 1.2 is out. Upgrade.", "Version 1.2 is out.", "Upgrade."},
	}
	for _, c := range cases {
		if first, rest := splitDoc(c.in); first != c.first || rest != c.rest {
			t.Errorf("splitDoc(%q) = %q, %q; want %q, %q", c.in, first, rest, c.first, c.rest)
		}
	}
}

func TestDocKeys(t *testing.T) {
	for name, want := range map[string]string{
		"example.com/a/notes.(*NotesController).Get-fm": "example.com/a/notes.(*NotesController).Get",
		"example.com/a/notes.Notes.Get-fm":              "example.com/a/notes.Notes.Get",
		"example.com/a/notes.(*Repo[...]).Get-fm":       "example.com/a/notes.(*Repo).Get",
		"main.(*Ctl).List-fm":                           "main.(*Ctl).List",
		"example.com/a/v1%2e2.listNotes":                "example.com/a/v1.2.listNotes",
	} {
		if got := handlerKey(name); got != want {
			t.Errorf("handlerKey(%s) = %s, want %s", name, got, want)
		}
	}
	if got := typeKey(reflect.TypeOf(docPage[int]{})); got != docPkg+".docPage" {
		t.Errorf("typeKey(generic) = %s", got)
	}
	if got := typeKey(reflect.TypeOf(struct{ A int }{})); got != "" {
		t.Errorf("typeKey(anonymous) = %s", got)
	}
}
