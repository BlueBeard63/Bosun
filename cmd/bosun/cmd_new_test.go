package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// chdir switches the working directory for the rest of the test; `bosun new`
// writes relative to it.
func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

func runCmd(t *testing.T, cmd *cobra.Command, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestNewServiceRefusesExistingDir(t *testing.T) {
	chdir(t, t.TempDir())
	if err := os.MkdirAll("api", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("api", "main.go"), []byte("package main // mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := runCmd(t, newServiceCmd(), "api", "--yes")
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("err = %v, want a refusal mentioning --force", err)
	}
	if src, _ := os.ReadFile(filepath.Join("api", "main.go")); string(src) != "package main // mine\n" {
		t.Fatalf("main.go was overwritten:\n%s", src)
	}
}

// TestNewServiceJoinsWorkspace runs `bosun new service` inside a project made
// by `bosun new project`: the service lands under services/, gets the
// workspace's module prefix and contracts replace, and is added to go.work.
func TestNewServiceJoinsWorkspace(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the go tool")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not found")
	}
	bosunRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(bosunRoot, "app.go")); err != nil {
		t.Skip("bosun source not found at repo root")
	}

	tmp := t.TempDir()
	chdir(t, tmp)
	if out, err := runCmd(t, newProjectCmd(), "acme", "--yes", "--module", "example.com/acme", "--bosun-dir", bosunRoot); err != nil || strings.Contains(out, "warning") {
		t.Fatalf("new project: %v\n%s", err, out)
	}
	chdir(t, filepath.Join(tmp, "acme", "services", "api"))
	if out, err := runCmd(t, newServiceCmd(), "orders", "--yes", "--event", "redis", "--bosun-dir", bosunRoot); err != nil || strings.Contains(out, "warning") {
		t.Fatalf("new service: %v\n%s", err, out)
	}

	gomod, err := os.ReadFile(filepath.Join(tmp, "acme", "services", "orders", "go.mod"))
	if err != nil {
		t.Fatalf("service not created under services/: %v", err)
	}
	if !strings.Contains(string(gomod), "module example.com/acme/services/orders") ||
		!strings.Contains(string(gomod), "replace example.com/acme/contracts => ../../contracts") { // as tidied
		t.Fatalf("service go.mod:\n%s", gomod)
	}
	gowork, _ := os.ReadFile(filepath.Join(tmp, "acme", "go.work"))
	if !strings.Contains(string(gowork), "./services/orders") {
		t.Fatalf("go.work does not use the new service:\n%s", gowork)
	}
	if _, err := os.Stat("orders"); err == nil {
		t.Fatal("service was also written to the current directory")
	}
}
