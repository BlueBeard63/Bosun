package bosun

import (
	"context"
	"net/http"
	"testing"
)

type docCtrl struct{}

var _ = Controller[docCtrl]("/routedoc", Doc("controller", "applies below"))

func (c *docCtrl) Routes(r *Router) {
	Get(r, "/a", c.get, Doc("Get a thing", "Returns it."))
	Get(r, "/b", c.get)
	r.Get("/raw", func(http.ResponseWriter, *http.Request) {}, Doc("Raw page", ""))
}

func (c *docCtrl) get(context.Context, *Req[struct{}]) (struct{}, error) { return struct{}{}, nil }

func TestDocMeta(t *testing.T) {
	app := newStarted(t)
	got := map[string]RouteDoc{}
	for _, rt := range app.Routes() {
		if d, ok := rt.Meta[MetaDoc].(RouteDoc); ok {
			got[rt.Path] = d
		}
	}
	want := map[string]RouteDoc{
		"/routedoc/a":   {Summary: "Get a thing", Description: "Returns it."},
		"/routedoc/b":   {Summary: "controller", Description: "applies below"},
		"/routedoc/raw": {Summary: "Raw page"},
	}
	for path, w := range want {
		if got[path] != w {
			t.Errorf("%s: Meta[MetaDoc] = %+v, want %+v", path, got[path], w)
		}
	}
}

func TestDocComments(t *testing.T) {
	if DocComment("example.com/x.Missing") != "" {
		t.Fatal("unregistered key should be empty")
	}
	RegisterDocComments(map[string]string{"example.com/x.T": "T is a thing.\n"})
	RegisterDocComments(map[string]string{"example.com/x.T.F": " F is a field. "})
	if got := DocComment("example.com/x.T"); got != "T is a thing." {
		t.Fatalf("DocComment = %q", got)
	}
	if got := DocComment("example.com/x.T.F"); got != "F is a field." {
		t.Fatalf("later registration: DocComment = %q", got)
	}
	RegisterDocComments(map[string]string{"example.com/x.T": "T is replaced."})
	if got := DocComment("example.com/x.T"); got != "T is replaced." {
		t.Fatalf("replacement: DocComment = %q", got)
	}
}
