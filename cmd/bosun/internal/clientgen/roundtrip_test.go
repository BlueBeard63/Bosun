package clientgen_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/cmd/bosun/internal/clientgen"
	"github.com/bluebeard63/bosun/modules/manifestmod"
)

type rtController struct{}

var _ = bosun.Controller[rtController]("/rt")

func (c *rtController) Routes(r *bosun.Router) {
	bosun.Get(r, "/users/{id}", c.get)
	bosun.Put(r, "/users/{id}", c.update)
	bosun.Post(r, "/forms", c.form)
	bosun.Post(r, "/text", c.text)
	bosun.Get(r, "/files/{path...}", c.files)
	bosun.Get(r, "/hello", c.hello)
}

// rawBodyHeader carries the request body as the client sent it to the
// handler; the test server copies it there before the app binds the body.
const rawBodyHeader = "X-Test-Raw-Body"

func received(req *http.Request, e Echo) (Echo, error) {
	e.Query = req.URL.RawQuery
	e.ContentType = req.Header.Get("Content-Type")
	e.Body = req.Header.Get(rawBodyHeader)
	return e, nil
}

func (c *rtController) get(_ context.Context, req *bosun.Req[GetUserIn]) (Echo, error) {
	in := req.Body
	return received(req.Request, Echo{ID: in.ID, Limit: in.Limit, Verbose: in.Verbose, Token: in.Token})
}

func (c *rtController) update(_ context.Context, req *bosun.Req[UpdateUserIn]) (Echo, error) {
	in := req.Body
	return received(req.Request, Echo{ID: in.ID, Dry: in.Dry, Token: in.Token, Ratio: in.Ratio, Name: in.Name, Age: in.Age})
}

func (c *rtController) form(_ context.Context, req *bosun.Req[FormIn]) (Echo, error) {
	in := req.Body
	return received(req.Request, Echo{Title: in.Title, Count: in.Count, Q: in.Q})
}

func (c *rtController) text(_ context.Context, req *bosun.Req[string]) (Echo, error) {
	return received(req.Request, Echo{Text: req.Body})
}

func (c *rtController) files(_ context.Context, req *bosun.Req[struct{}]) (Echo, error) {
	return received(req.Request, Echo{ID: req.Params["path"]})
}

func (c *rtController) hello(context.Context, *bosun.Req[struct{}]) (string, error) {
	return "hello, world", nil
}

// clientMain calls every route through the generated client and prints one
// "name json" line per call.
const clientMain = `package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"example.com/rt/client"
	"example.com/rt/contracts"
)

func main() {
	c := client.NewRtClient(os.Args[1])
	ctx := context.Background()
	show := func(name string, v any, err error) {
		if err != nil {
			fmt.Printf("%s %q\n", name, err.Error())
			return
		}
		b, _ := json.Marshal(v)
		fmt.Printf("%s %s\n", name, b)
	}
	e, err := c.Get(ctx, "a b", contracts.GetUserIn{Limit: 5, Verbose: true, Token: "t1"})
	show("get", e, err)
	e, err = c.Update(ctx, "", contracts.UpdateUserIn{ID: "u1", Dry: true, Token: "t2", Ratio: 0.5, Name: "Ann", Age: 30})
	show("update", e, err)
	e, err = c.Form(ctx, contracts.FormIn{Title: "Hi there", Count: 3, Q: "x"})
	show("form", e, err)
	e, err = c.Text(ctx, "plain \"text\"")
	show("text", e, err)
	e, err = c.Files(ctx, "docs/a b.txt")
	show("files", e, err)
	s, err := c.Hello(ctx)
	show("hello", s, err)
}
`

// TestRoundTrip generates a client from a real Bosun app's manifest, then
// runs it against that app and checks each field arrived where the server
// binds it: path, query, header, form or JSON body.
func TestRoundTrip(t *testing.T) {
	t.Parallel()
	app := bosun.New()
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.Header.Set(rawBodyHeader, string(raw))
		app.ServeHTTP(w, r)
	}))
	defer srv.Close()

	// The test's types are copied into example.com/rt/contracts, so point the
	// manifest's imports there.
	const contracts = "example.com/rt/contracts"
	testPkg := reflect.TypeOf(Echo{}).PkgPath()
	var routes []clientgen.Route
	for _, r := range manifestmod.BuildFor(app, manifestmod.Info{Service: "rt"}).Routes {
		if !strings.HasPrefix(r.Path, "/rt/") {
			continue
		}
		route := clientgen.Route{Method: r.Method, Path: r.Path, Operation: r.Operation, In: r.In, Out: r.Out}
		if r.InImport == testPkg {
			route.InImport = contracts
		}
		if r.OutImport == testPkg {
			route.OutImport = contracts
		}
		routes = append(routes, route)
	}
	src, err := clientgen.Generate("client", "rt", routes)
	if err != nil {
		t.Fatalf("generate: %v\n%s", err, src)
	}

	types, err := os.ReadFile("roundtrip_types_test.go")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/rt\n\ngo 1.22\n")
	writeFile(t, filepath.Join(dir, "contracts", "contracts.go"), strings.Replace(string(types), "package clientgen_test", "package contracts", 1))
	writeFile(t, filepath.Join(dir, "client", "client.go"), src)
	writeFile(t, filepath.Join(dir, "main.go"), clientMain)
	out := goRun(t, dir, srv.URL)

	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		name, val, _ := strings.Cut(strings.TrimSpace(line), " ")
		got[name] = val
	}
	echo := func(name string) Echo {
		t.Helper()
		var e Echo
		if err := json.Unmarshal([]byte(got[name]), &e); err != nil {
			t.Fatalf("%s: %s (%v)\n--- client ---\n%s", name, got[name], err, src)
		}
		return e
	}

	want := map[string]Echo{
		// Path, query and header fields; a GET sends no body.
		"get": {ID: "a b", Limit: 5, Verbose: true, Token: "t1", Query: "limit=5&verbose=true"},
		// The path comes from the In field when the argument is empty, and only
		// the untagged fields form the JSON body.
		"update": {ID: "u1", Dry: true, Token: "t2", Ratio: 0.5, Name: "Ann", Age: 30,
			Query: "dry=true&ratio=0.5", ContentType: "application/json", Body: `{"age":30,"name":"Ann"}`},
		"form": {Title: "Hi there", Count: 3, Q: "x", Query: "q=x",
			ContentType: "application/x-www-form-urlencoded", Body: "count=3&title=Hi+there"},
		// A string In is the raw body, not a JSON string.
		"text": {Text: `plain "text"`, ContentType: "text/plain; charset=utf-8", Body: `plain "text"`},
		// A wildcard keeps its slashes.
		"files": {ID: "docs/a b.txt"},
	}
	for name, w := range want {
		if e := echo(name); e != w {
			t.Errorf("%s:\n got %+v\nwant %+v", name, e, w)
		}
	}
	if got["hello"] != `"hello, world"` {
		t.Errorf("hello = %s, want the raw text body", got["hello"])
	}
	if t.Failed() {
		t.Logf("--- client ---\n%s", src)
	}
}

// goRun runs the main package of the module at dir with args and returns its
// output.
func goRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("runs the go tool")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool not found")
	}
	cmd := exec.Command(goBin, append([]string{"run", "."}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod", "GOPROXY=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run: %v\n%s", err, out)
	}
	return string(out)
}
