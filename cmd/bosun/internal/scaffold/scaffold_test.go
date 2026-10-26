package scaffold_test

import (
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bluebeard63/bosun/cmd/bosun/internal/scaffold"
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
	if len(written) != 5 {
		t.Fatalf("wrote %d files, want 5", len(written))
	}
	for _, rel := range []string{"go.mod", "main.go", "internal/api/api.go", "Dockerfile", "zz_bosun_registry.go"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Fatalf("missing %s: %v", rel, err)
		}
	}
	mustParse(t, filepath.Join(dir, "main.go"))
	mustParse(t, filepath.Join(dir, "internal/api/api.go"))
	mustParse(t, filepath.Join(dir, "zz_bosun_registry.go"))

	mainSrc, _ := os.ReadFile(filepath.Join(dir, "main.go"))
	if strings.Contains(string(mainSrc), "internal/api") || !strings.Contains(string(mainSrc), "//go:generate bosun gen registry") {
		t.Fatalf("main.go should rely on the generated registry:\n%s", mainSrc)
	}
	reg, _ := os.ReadFile(filepath.Join(dir, "zz_bosun_registry.go"))
	if !strings.Contains(string(reg), `_ "example.com/billing/internal/api" // Controller[HelloController]`) {
		t.Fatalf("registry missing internal/api:\n%s", reg)
	}

	api, _ := os.ReadFile(filepath.Join(dir, "internal/api/api.go"))
	if !strings.Contains(string(api), "eventmemmod.Default()") {
		t.Fatalf("inmem event wiring missing:\n%s", api)
	}
	gomod, _ := os.ReadFile(filepath.Join(dir, "go.mod"))
	if !strings.Contains(string(gomod), "module example.com/billing") {
		t.Fatalf("go.mod module wrong:\n%s", gomod)
	}
	if strings.Contains(string(gomod), "v0.0.0") {
		t.Fatalf("go.mod pins placeholder bosun version:\n%s", gomod)
	}
	if !strings.Contains(string(gomod), "\tgithub.com/bluebeard63/bosun v") {
		t.Fatalf("go.mod missing bosun require with a real version:\n%s", gomod)
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
	// The driver is its own module and needs Go 1.25, so go.mod requires it
	// and the Dockerfile builds with a toolchain that satisfies the go line.
	gomod, _ := os.ReadFile(filepath.Join(dir, "go.mod"))
	if !strings.Contains(string(gomod), "\tgithub.com/bluebeard63/bosun/modules/eventnatsmod v") ||
		!strings.Contains(string(gomod), "\ngo 1.25.0\n") {
		t.Fatalf("go.mod missing driver require or go line:\n%s", gomod)
	}
	df, _ := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	if !strings.HasPrefix(string(df), "FROM golang:1.25 AS build") {
		t.Fatalf("Dockerfile image does not match go.mod:\n%s", df)
	}
}

// bosunRoot returns the bosun checkout these tests build scaffolds against
// (via Service.LocalBosun), so they verify the templates against the current
// API rather than the last release.
func bosunRoot(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("runs the go tool")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not found")
	}
	root, err := filepath.Abs("../../../..") // cmd/bosun/internal/scaffold -> repo
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "app.go")); err != nil {
		t.Skip("bosun source not found at repo root")
	}
	return root
}

// goRun runs the go tool in dir outside any enclosing workspace (CI creates
// one at the repo root).
func goRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

// TestScaffoldedServiceBuilds scaffolds a service for every event backend,
// tidies it the way `bosun new` does, and builds and vets the result.
func TestScaffoldedServiceBuilds(t *testing.T) {
	root := bosunRoot(t)
	for _, event := range []string{"inmem", "amqp", "nats", "redis", "none"} {
		t.Run(event, func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(t.TempDir(), "svc")
			if _, err := scaffold.WriteService(dir, scaffold.Service{
				Module: "example.com/svc", Name: "svc", Port: 8080, Event: event, LocalBosun: root,
			}); err != nil {
				t.Fatalf("write: %v", err)
			}
			if err := scaffold.Tidy(dir); err != nil {
				t.Fatalf("tidy: %v", err)
			}
			// No go.sum check: with bosun replaced by a directory, the inmem
			// and none backends have no checksummed dependencies at all.
			goRun(t, dir, "build", "./...")
			goRun(t, dir, "vet", "./...")

			gomod, _ := os.ReadFile(filepath.Join(dir, "go.mod"))
			m := regexp.MustCompile(`(?m)^go (\d+\.\d+)`).FindSubmatch(gomod)
			if m == nil {
				t.Fatalf("no go directive:\n%s", gomod)
			}
			df, _ := os.ReadFile(filepath.Join(dir, "Dockerfile"))
			if !strings.HasPrefix(string(df), "FROM golang:"+string(m[1])+" AS build") {
				t.Fatalf("Dockerfile image does not match go %s:\n%s", m[1], df)
			}
		})
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
