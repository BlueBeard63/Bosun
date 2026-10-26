package registrygen

import (
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

const fixture = "testdata/shop"

func scanFixture(t *testing.T) *Module {
	t.Helper()
	m, err := Scan(fixture)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestScanFindsRegisteringPackages(t *testing.T) {
	m := scanFixture(t)
	if m.Path != "example.com/shop" {
		t.Fatalf("module path = %q", m.Path)
	}
	got := map[string][]string{}
	for _, p := range m.Packages {
		got[p.ImportPath] = p.Decls
	}
	want := map[string][]string{
		// aliased import, var block, multiple type params
		"example.com/shop/internal/billing": {"Service[memStore]", "DefaultBind[Store, memStore]", "Middleware[Auth]"},
		// despite aaa_tool.go, an ignored package main file that sorts first
		"example.com/shop/internal/users": {"Service[Repo]", "Controller[UsersController]"},
	}
	// Not included: internal/dto (imports bosun, registers nothing),
	// internal/testsonly (_test.go only), pkg/util (no bosun), nested
	// (own go.mod), vendor, _scratch.
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("packages = %v\nwant %v", got, want)
	}
	if !reflect.DeepEqual(m.Skipped, []string{"example.com/shop/cmd/worker"}) {
		t.Fatalf("skipped = %v", m.Skipped)
	}
}

func TestRenderGolden(t *testing.T) {
	m := scanFixture(t)
	target, err := TargetFor(m, fixture)
	if err != nil {
		t.Fatal(err)
	}
	if target != (Options{PkgName: "main", ImportPath: "example.com/shop"}) {
		t.Fatalf("target = %+v", target)
	}
	src, err := Render(m, target)
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "shop.golden")
	if *update {
		if err := os.WriteFile(golden, src, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	// A checkout with core.autocrlf=true has a CRLF golden; Render emits LF.
	if string(src) != strings.ReplaceAll(string(want), "\r\n", "\n") {
		t.Fatalf("generated file differs from golden (run with -update):\n%s", src)
	}
	// Deterministic: rendering again yields identical bytes.
	again, _ := Render(scanFixture(t), target)
	if string(again) != string(src) {
		t.Fatal("output is not deterministic")
	}
}

func TestScanBuildTags(t *testing.T) {
	dir := t.TempDir()
	reg := "import \"github.com/bluebeard63/bosun\"\n\ntype X struct{}\n\nvar _ = bosun.Service[X]()\n"
	for rel, body := range map[string]string{
		"go.mod":            "module example.com/app\n\ngo 1.22\n",
		"main.go":           "package main\n\nfunc main() {}\n",
		"extra/extra.go":    "//go:build extra\n\npackage extra\n\n" + reg,
		"plain/plain.go":    "package plain\n\n" + reg,
		"plain/aa_never.go": "//go:build never\n\npackage plain\n\nimport \"github.com/bluebeard63/bosun\"\n\ntype Y struct{}\n\nvar _ = bosun.Service[Y]()\n",
	} {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	scan := func(tags ...string) map[string][]string {
		m, err := Scan(dir, tags...)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string][]string{}
		for _, p := range m.Packages {
			got[p.ImportPath] = p.Decls
		}
		return got
	}
	want := map[string][]string{"example.com/app/plain": {"Service[X]"}}
	if got := scan(); !reflect.DeepEqual(got, want) {
		t.Fatalf("no tags: %v, want %v", got, want)
	}
	want["example.com/app/extra"] = []string{"Service[X]"}
	if got := scan("extra"); !reflect.DeepEqual(got, want) {
		t.Fatalf("tags=extra: %v, want %v", got, want)
	}
}

func TestRenderSkipsTargetPackage(t *testing.T) {
	m := scanFixture(t)
	src, err := Render(m, Options{PkgName: "users", ImportPath: "example.com/shop/internal/users"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), `"example.com/shop/internal/users"`) {
		t.Fatalf("target imported itself:\n%s", src)
	}
	if !strings.Contains(string(src), `_ "example.com/shop/internal/billing"`) {
		t.Fatalf("sibling missing:\n%s", src)
	}
}

func TestRenderRejectsUnimportableInternal(t *testing.T) {
	m := &Module{Path: "example.com/shop", Packages: []Package{
		{ImportPath: "example.com/shop/svc/internal/a", Decls: []string{"Service[A]"}},
	}}
	_, err := Render(m, Options{PkgName: "util", ImportPath: "example.com/shop/pkg/util"})
	if !errors.Is(err, ErrInternalImport) {
		t.Fatalf("err = %v, want ErrInternalImport", err)
	}
}

func TestInternalAllowed(t *testing.T) {
	cases := []struct {
		importer, imported string
		ok                 bool
	}{
		{"example.com/shop", "example.com/shop/internal/users", true},
		{"example.com/shop/cmd/api", "example.com/shop/internal/users", true},
		{"example.com/shop/internal/app", "example.com/shop/internal/users", true},
		{"example.com/shop/svc", "example.com/shop/svc/internal/x", true},
		{"example.com/shop/pkg", "example.com/shop/svc/internal/x", false},
		{"example.com/other", "example.com/shop/internal/users", false},
		{"example.com/shop", "example.com/shop/pkg/util", true},
	}
	for _, c := range cases {
		if got := internalAllowed(c.importer, c.imported); got != c.ok {
			t.Errorf("internalAllowed(%s, %s) = %v", c.importer, c.imported, got)
		}
	}
}

func TestTargetForPackageNames(t *testing.T) {
	m := scanFixture(t)
	for dir, want := range map[string]Options{
		filepath.Join(fixture, "pkg/util"):     {PkgName: "util", ImportPath: "example.com/shop/pkg/util"},
		filepath.Join(fixture, "internal/dto"): {PkgName: "dto", ImportPath: "example.com/shop/internal/dto"},
	} {
		got, err := TargetFor(m, dir)
		if err != nil || got != want {
			t.Errorf("TargetFor(%s) = %+v, %v; want %+v", dir, got, err, want)
		}
	}
	empty := filepath.Join(t.TempDir(), "fresh")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := TargetFor(m, empty); err == nil {
		t.Fatal("a directory outside the module should be rejected")
	}
}

func TestFindModuleRoot(t *testing.T) {
	root, err := FindModuleRoot(filepath.Join(fixture, "internal", "users"))
	if err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs(fixture)
	if root != abs {
		t.Fatalf("root = %s, want %s", root, abs)
	}
}

func TestScanDocs(t *testing.T) {
	m := scanFixture(t)
	if !m.DocsSupported {
		t.Fatal("fixture requires bosun " + DocsMinVersion + ": docs should be supported")
	}
	got := map[string]map[string]string{}
	for _, pd := range m.Docs {
		got[pd.ImportPath+" ("+pd.Name+")"] = pd.Comments
	}
	want := map[string]map[string]string{
		"example.com/shop (main)":            {"Banner": "Banner is printed at start; package main docs are keyed \"main.\"."},
		"example.com/shop/cmd/worker (main)": {"Job": "Job is in another binary's main package, so its docs are left out."},
		"example.com/shop/internal/dto (dto)": {
			"User":      "User is a user as the API returns it.",
			"User.ID":   "ID is the user's key.",
			"User.Name": "Display name.",
			"UserID":    "UserID selects a user.",
			"UserID.ID": "The user's key.",
		},
		"example.com/shop/internal/users (users)": {
			"UsersController":         "UsersController serves /users.",
			"UsersController.Repo":    "Injected.",
			"(*UsersController).List": "List returns every user's name.\n\nThe list is not paged.",
			"UsersController.Get":     "Get returns one user. Unknown IDs get 404.",
		},
		"example.com/shop/pkg/util (util)": {
			"Pair":   "Pair is two ints, in a package that doesn't import bosun.",
			"Pair.A": "The values.",
			"Pair.B": "The values.",
		},
	}
	// Not collected: Routes and helper (not typed handlers), the Page
	// alias, unexported and uncommented fields, uncommented types.
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("docs = %v\nwant %v", got, want)
	}
}

func TestDocsSupported(t *testing.T) {
	cases := map[string]bool{
		"module x\nrequire github.com/bluebeard63/bosun v0.8.0\n":                                                                          true,
		"module x\nrequire github.com/bluebeard63/bosun v1.0.0 // indirect\n":                                                              true,
		"module x\nrequire (\n\tgithub.com/other v1.0.0\n\tgithub.com/bluebeard63/bosun v0.9.2\n)\n":                                       true,
		"module x\nrequire github.com/bluebeard63/bosun v0.7.0\n":                                                                          false,
		"module x\nrequire github.com/bluebeard63/bosun v0.8.0-rc.1\n":                                                                     false,
		"module x\nrequire github.com/bluebeard63/bosun v0.8.1-0.20261001000000-abcdef012345\n":                                            true,
		"module x\nrequire github.com/bluebeard63/bosun v0.0.0\nreplace github.com/bluebeard63/bosun => ../bosun\n":                        true,
		"module x\nrequire github.com/bluebeard63/bosun v0.7.0\nreplace (\n\tgithub.com/bluebeard63/bosun v0.7.0 => \"C:/src/bosun\"\n)\n": true,
		"module x\nrequire github.com/bluebeard63/bosun/cmd/bosun v0.9.0\n":                                                                false,
		"module x\n": false,
	}
	for gomod, want := range cases {
		if got := docsSupported([]byte(strings.ReplaceAll(gomod, "\n", "\r\n"))); got != want {
			t.Errorf("docsSupported(%q) = %v, want %v", gomod, got, want)
		}
	}
}

func TestRenderWithoutDocs(t *testing.T) {
	m := scanFixture(t)
	target, _ := TargetFor(m, fixture)
	for name, mod := range map[string]func(*Module, *Options){
		"NoDocs":             func(_ *Module, o *Options) { o.NoDocs = true },
		"older bosun":        func(m *Module, _ *Options) { m.DocsSupported = false },
		"no comments at all": func(m *Module, _ *Options) { m.Docs = nil },
	} {
		m := scanFixture(t)
		opts := target
		mod(m, &opts)
		src, err := Render(m, opts)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), "RegisterDocComments") || strings.Contains(string(src), `"github.com/bluebeard63/bosun"`) {
			t.Errorf("%s: docs rendered:\n%s", name, src)
		}
	}
}

// generateFixture copies the fixture into a temporary module that replaces
// bosun with this repository and writes the generated file into it.
func generateFixture(t *testing.T) (dir, goBin string) {
	t.Helper()
	if testing.Short() {
		t.Skip("runs the go tool")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool not found")
	}
	repoRoot, err := filepath.Abs("../../../..") // cmd/bosun/internal/registrygen -> repo
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "app.go")); err != nil {
		t.Skip("bosun source not found at repo root")
	}

	dir = t.TempDir()
	copyTree(t, fixture, dir, func(rel string) bool {
		top := strings.Split(rel, string(filepath.Separator))[0]
		return top == "vendor" || top == "_scratch" || top == "nested"
	})
	// Quoted, so a repo path with spaces or a Windows drive letter parses.
	gomod := "module example.com/shop\n\ngo 1.22\n\nrequire github.com/bluebeard63/bosun v0.0.0\n\nreplace github.com/bluebeard63/bosun => " +
		strconv.Quote(filepath.ToSlash(repoRoot)) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := TargetFor(m, dir)
	src, err := Render(m, target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), src, 0o644); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "RegisterDocComments") {
		t.Fatalf("a module replacing bosun should get doc comments:\n%s", src)
	}
	return dir, goBin
}

// TestGeneratedFileCompiles builds the fixture's main package with the
// generated file against this repository's bosun module.
func TestGeneratedFileCompiles(t *testing.T) {
	dir, goBin := generateFixture(t)
	src, _ := os.ReadFile(filepath.Join(dir, FileName))
	cmd := exec.Command(goBin, "build", "-o", os.DevNull, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated program does not build: %v\n%s\n--- %s ---\n%s", err, out, FileName, src)
	}
}

// docsSpecTest runs inside the generated fixture module: the doc comments
// the registry registers must reach the OpenAPI spec.
const docsSpecTest = `package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/bluebeard63/bosun"
	_ "github.com/bluebeard63/bosun/openapi"
)

func TestDocsInSpec(t *testing.T) {
	app := bosun.New()
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest("GET", "/openapi.json", nil))
	var spec struct {
		Paths      map[string]map[string]map[string]any
		Components struct{ Schemas map[string]map[string]any }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatal(err)
	}
	ops := map[string]map[string]any{}
	for _, item := range spec.Paths {
		for _, op := range item {
			ops[op["operationId"].(string)] = op
		}
	}
	check := func(what string, got any, want string) {
		t.Helper()
		if got != want {
			t.Errorf("%s = %#v, want %q", what, got, want)
		}
	}
	check("UsersList summary", ops["UsersList"]["summary"], "List returns every user's name.")
	check("UsersList description", ops["UsersList"]["description"], "The list is not paged.")
	check("UsersGet summary", ops["UsersGet"]["summary"], "Get returns one user.")
	check("UsersGet description", ops["UsersGet"]["description"], "Unknown IDs get 404.")
	user := spec.Components.Schemas["DtoUser"]
	check("DtoUser description", user["description"], "User is a user as the API returns it.")
	props := user["properties"].(map[string]any)
	check("id description", props["id"].(map[string]any)["description"], "ID is the user's key.")
	check("name description", props["name"].(map[string]any)["description"], "Display name.")
	if _, ok := props["bio"].(map[string]any)["description"]; ok {
		t.Error("uncommented field has a description")
	}
}
`

// TestGeneratedDocsReachSpec runs a test in the generated fixture module
// that reads its OpenAPI spec: handler, type and field comments collected
// by the generator appear as summaries and descriptions.
func TestGeneratedDocsReachSpec(t *testing.T) {
	dir, goBin := generateFixture(t)
	if err := os.WriteFile(filepath.Join(dir, "docs_spec_test.go"), []byte(docsSpecTest), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(goBin, "test", "-count=1", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("spec test in the generated module failed: %v\n%s", err, out)
	}
}

func copyTree(t *testing.T, src, dst string, skip func(rel string) bool) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if rel != "." && skip(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}
