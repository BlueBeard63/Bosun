package registrygen

import (
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
		"example.com/shop/internal/users":   {"Service[Repo]", "Controller[UsersController]"},
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
	if string(src) != string(want) {
		t.Fatalf("generated file differs from golden (run with -update):\n%s", src)
	}
	// Deterministic: rendering again yields identical bytes.
	again, _ := Render(scanFixture(t), target)
	if string(again) != string(src) {
		t.Fatal("output is not deterministic")
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

// TestGeneratedFileCompiles builds the fixture's main package with the
// generated file against this repository's bosun module.
func TestGeneratedFileCompiles(t *testing.T) {
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

	dir := t.TempDir()
	copyTree(t, fixture, dir, func(rel string) bool {
		top := strings.Split(rel, string(filepath.Separator))[0]
		return top == "vendor" || top == "_scratch" || top == "nested"
	})
	gomod := "module example.com/shop\n\ngo 1.22\n\nrequire github.com/bluebeard63/bosun v0.0.0\n\nreplace github.com/bluebeard63/bosun => " + repoRoot + "\n"
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
	cmd := exec.Command(goBin, "build", "-o", os.DevNull, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated program does not build: %v\n%s\n--- %s ---\n%s", err, out, FileName, src)
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
