package scaffold_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amberstack/bosun/cmd/bosun/internal/scaffold"
)

func mustParse(t *testing.T, path string) {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), path, src, parser.AllErrors); err != nil {
		t.Fatalf("%s does not parse: %v\n%s", path, err, src)
	}
}

func TestWriteService(t *testing.T) {
	dir := t.TempDir()
	written, err := scaffold.WriteService(dir, scaffold.Service{
		Module: "example.com/billing", Name: "billing", Port: 8080, Event: "inmem",
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(written) != 4 {
		t.Fatalf("wrote %d files, want 4", len(written))
	}
	for _, rel := range []string{"go.mod", "main.go", "internal/api/api.go", "Dockerfile"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Fatalf("missing %s: %v", rel, err)
		}
	}
	mustParse(t, filepath.Join(dir, "main.go"))
	mustParse(t, filepath.Join(dir, "internal/api/api.go"))

	api, _ := os.ReadFile(filepath.Join(dir, "internal/api/api.go"))
	if !strings.Contains(string(api), "eventmemmod.Default()") {
		t.Fatalf("inmem event wiring missing:\n%s", api)
	}
	gomod, _ := os.ReadFile(filepath.Join(dir, "go.mod"))
	if !strings.Contains(string(gomod), "module example.com/billing") {
		t.Fatalf("go.mod module wrong:\n%s", gomod)
	}
}

func TestWriteServiceBrokerEvent(t *testing.T) {
	dir := t.TempDir()
	if _, err := scaffold.WriteService(dir, scaffold.Service{
		Module: "example.com/orders", Name: "orders", Port: 8081, Event: "nats",
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	mustParse(t, filepath.Join(dir, "internal/api/api.go"))
	api, _ := os.ReadFile(filepath.Join(dir, "internal/api/api.go"))
	if !strings.Contains(string(api), "eventnatsmod.Use()") {
		t.Fatalf("nats event wiring missing:\n%s", api)
	}
}

func TestWriteProject(t *testing.T) {
	dir := t.TempDir()
	written, err := scaffold.WriteProject(dir, scaffold.Project{
		Module: "example.com/acme", Name: "acme", Service: "api", Port: 8080, Event: "inmem",
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(written) == 0 {
		t.Fatal("no files written")
	}
	for _, rel := range []string{"go.work", "contracts/doc.go", "README.md", "services/api/main.go", "services/api/go.mod"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Fatalf("missing %s: %v", rel, err)
		}
	}
	mustParse(t, filepath.Join(dir, "contracts/doc.go"))
	mustParse(t, filepath.Join(dir, "services/api/main.go"))

	gowork, _ := os.ReadFile(filepath.Join(dir, "go.work"))
	if !strings.Contains(string(gowork), "./services/api") {
		t.Fatalf("go.work missing service use:\n%s", gowork)
	}
	svcMod, _ := os.ReadFile(filepath.Join(dir, "services/api/go.mod"))
	if !strings.Contains(string(svcMod), "module example.com/acme/services/api") {
		t.Fatalf("service module path wrong:\n%s", svcMod)
	}
}
