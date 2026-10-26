package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// localBosun returns the bosun checkout to build scaffolds against, or skips.
func localBosun(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("runs the go tool")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not found")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "app.go")); err != nil {
		t.Skip("bosun source not found at repo root")
	}
	return root
}

func goTool(t *testing.T, dir, gowork string, args ...string) {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK="+gowork)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

// TestNewMonoThenAdd runs `bosun new mono --bare` and then each `bosun add`
// generator inside it, and builds and vets the result.
func TestNewMonoThenAdd(t *testing.T) {
	bosunRoot := localBosun(t)
	tmp := t.TempDir()
	chdir(t, tmp)
	if out, err := runCmd(t, newCmd(), "mono", "shop", "--bare", "--yes", "--bosun-dir", bosunRoot); err != nil || strings.Contains(out, "warning") {
		t.Fatalf("new mono: %v\n%s", err, out)
	}
	shop := filepath.Join(tmp, "shop")
	chdir(t, filepath.Join(shop))
	for _, args := range [][]string{
		{"service", "catalog"},
		{"controller", "catalog", "--prefix", "/products"},
		{"middleware", "request-log"},
	} {
		out, err := runCmd(t, addCmd(), args...)
		if err != nil || !strings.Contains(out, "Updated zz_bosun_registry.go") {
			t.Fatalf("add %v: %v\n%s", args, err, out)
		}
	}
	if _, err := runCmd(t, addCmd(), "service", "catalog"); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("re-adding: err = %v, want a refusal mentioning --force", err)
	}
	reg, _ := os.ReadFile(filepath.Join(shop, "zz_bosun_registry.go"))
	if !strings.Contains(string(reg), `"example.com/shop/internal/catalog"`) || !strings.Contains(string(reg), `"example.com/shop/internal/middleware"`) {
		t.Fatalf("registry:\n%s", reg)
	}
	goTool(t, shop, "off", "build", "./...")
	goTool(t, shop, "off", "vet", "./...")
}

// TestAddInWorkspace runs `bosun add` from the root of a bare workspace made
// by `bosun new project`: the component goes into its only service.
func TestAddInWorkspace(t *testing.T) {
	bosunRoot := localBosun(t)
	tmp := t.TempDir()
	chdir(t, tmp)
	if out, err := runCmd(t, newCmd(), "project", "acme", "--bare", "--yes", "--bosun-dir", bosunRoot); err != nil || strings.Contains(out, "warning") {
		t.Fatalf("new project: %v\n%s", err, out)
	}
	chdir(t, filepath.Join(tmp, "acme"))
	if out, err := runCmd(t, addCmd(), "controller", "orders"); err != nil {
		t.Fatalf("add controller: %v\n%s", err, out)
	}
	api := filepath.Join(tmp, "acme", "services", "api")
	if _, err := os.Stat(filepath.Join(api, "internal", "orders", "orders_controller.go")); err != nil {
		t.Fatalf("controller not in the service: %v", err)
	}
	goTool(t, api, "", "build", "./...")
	goTool(t, api, "", "vet", "./...")
}
