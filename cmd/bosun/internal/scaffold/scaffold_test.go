package scaffold_test

import (
	"errors"
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

	// contracts is its own module in the workspace, and the service reaches
	// it through a replace so it also builds with GOWORK=off.
	if !strings.Contains(string(gowork), "./contracts") {
		t.Fatalf("go.work missing contracts use:\n%s", gowork)
	}
	contractsMod, _ := os.ReadFile(filepath.Join(dir, "contracts/go.mod"))
	if !strings.Contains(string(contractsMod), "module example.com/acme/contracts") {
		t.Fatalf("contracts go.mod wrong:\n%s", contractsMod)
	}
	if !strings.Contains(string(svcMod), `replace example.com/acme/contracts => "../../contracts"`) {
		t.Fatalf("service go.mod missing contracts replace:\n%s", svcMod)
	}
	df, _ := os.ReadFile(filepath.Join(dir, "services/api/Dockerfile"))
	if !strings.Contains(string(df), "COPY contracts ./contracts") || !strings.Contains(string(df), "COPY services/api ./services/api") {
		t.Fatalf("workspace Dockerfile should build from the workspace root:\n%s", df)
	}
}

func TestFindWorkspace(t *testing.T) {
	dir := t.TempDir()
	if _, err := scaffold.WriteProject(dir, scaffold.Project{
		Module: "example.com/acme", Name: "acme", Service: "api", Port: 8080, Event: "inmem",
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	ws, err := scaffold.FindWorkspace(filepath.Join(dir, "services", "api", "internal"))
	if err != nil || ws == nil {
		t.Fatalf("FindWorkspace = %v, %v", ws, err)
	}
	root, _ := filepath.Abs(dir)
	if ws.Root != root || ws.Module != "example.com/acme" || ws.ContractsModule != "example.com/acme/contracts" ||
		ws.ContractsDir != filepath.Join(root, "contracts") {
		t.Fatalf("workspace = %+v", ws)
	}
	if ws, err := scaffold.FindWorkspace(t.TempDir()); err != nil || ws != nil {
		t.Fatalf("outside a workspace: %+v, %v", ws, err)
	}
}

func TestWriteServiceRefusesNonEmptyDir(t *testing.T) {
	dir := t.TempDir()
	mainGo := filepath.Join(dir, "main.go")
	if err := os.WriteFile(mainGo, []byte("package main // mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := scaffold.Service{Module: "example.com/api", Name: "api", Port: 8080, Event: "inmem"}
	if _, err := scaffold.WriteService(dir, svc); !errors.Is(err, scaffold.ErrExists) {
		t.Fatalf("err = %v, want ErrExists", err)
	}
	if src, _ := os.ReadFile(mainGo); string(src) != "package main // mine\n" {
		t.Fatalf("existing main.go was overwritten:\n%s", src)
	}
	if _, err := scaffold.WriteProject(dir, scaffold.Project{
		Module: "example.com/acme", Name: "acme", Service: "api", Port: 8080, Event: "inmem",
	}); !errors.Is(err, scaffold.ErrExists) {
		t.Fatalf("project err = %v, want ErrExists", err)
	}

	svc.Force = true
	if _, err := scaffold.WriteService(dir, svc); err != nil {
		t.Fatalf("with Force: %v", err)
	}
	if src, _ := os.ReadFile(mainGo); !strings.Contains(string(src), "bosun.New()") {
		t.Fatalf("Force did not overwrite main.go:\n%s", src)
	}
}

func TestWriteServiceValidates(t *testing.T) {
	for _, s := range []scaffold.Service{
		{Module: "example.com/x", Name: `a"b`, Port: 8080},
		{Module: "example.com/x", Name: "two words", Port: 8080},
		{Module: `example.com/"x`, Name: "x", Port: 8080},
		{Module: "example.com/my svc", Name: "x", Port: 8080},
		{Module: "example.com//x", Name: "x", Port: 8080},
		{Module: "example.com/x", Name: "x", Port: 8080, Event: "kafka"},
		{Module: "example.com/x", Name: "x", Port: 0},
	} {
		dir := t.TempDir()
		if _, err := scaffold.WriteService(dir, s); err == nil {
			t.Errorf("%+v: no error", s)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("%+v: wrote files before validating", s)
		}
	}
	if _, err := scaffold.WriteProject(t.TempDir(), scaffold.Project{
		Module: "example.com/acme", Name: "acme", Service: "my api", Port: 8080,
	}); err == nil {
		t.Error("project with an invalid service name: no error")
	}
}

// TestProjectWithSecondServiceBuilds scaffolds a project, adds a second
// service to its workspace the way `bosun new service` does, makes both
// services import a contracts type, and builds them in workspace mode and
// standalone (GOWORK=off, as in Docker).
func TestProjectWithSecondServiceBuilds(t *testing.T) {
	root := bosunRoot(t)
	dir := filepath.Join(t.TempDir(), "acme")
	if _, err := scaffold.WriteProject(dir, scaffold.Project{
		Module: "example.com/acme", Name: "acme", Service: "api", Port: 8080, Event: "inmem", LocalBosun: root,
	}); err != nil {
		t.Fatalf("project: %v", err)
	}
	ws, err := scaffold.FindWorkspace(filepath.Join(dir, "services", "api"))
	if err != nil || ws == nil {
		t.Fatalf("FindWorkspace = %v, %v", ws, err)
	}
	orders := filepath.Join(ws.Root, "services", "orders")
	if _, err := scaffold.WriteService(orders, scaffold.Service{
		Module: ws.Module + "/services/orders", Name: "orders", Port: 8081, Event: "nats",
		LocalBosun: root, Workspace: ws,
	}); err != nil {
		t.Fatalf("second service: %v", err)
	}

	contract := "package contracts\n\n// OrderPlaced is published by orders.\ntype OrderPlaced struct{ ID string }\n"
	if err := os.WriteFile(filepath.Join(dir, "contracts", "orders.go"), []byte(contract), 0o644); err != nil {
		t.Fatal(err)
	}
	use := "package api\n\nimport \"example.com/acme/contracts\"\n\nvar _ contracts.OrderPlaced\n"
	for _, svc := range []string{"api", "orders"} {
		svcDir := filepath.Join(dir, "services", svc)
		if err := os.WriteFile(filepath.Join(svcDir, "internal", "api", "contracts.go"), []byte(use), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := scaffold.Tidy(svcDir); err != nil {
			t.Fatalf("tidy %s: %v", svc, err)
		}
	}
	if err := ws.Use(orders); err != nil {
		t.Fatalf("go work use: %v", err)
	}
	gowork, _ := os.ReadFile(filepath.Join(dir, "go.work"))
	if !strings.Contains(string(gowork), "./services/orders") || !strings.Contains(string(gowork), "go 1.25.0") {
		t.Fatalf("go.work not updated for orders:\n%s", gowork)
	}

	for _, svc := range []string{"api", "orders"} {
		svcDir := filepath.Join(dir, "services", svc)
		goWork(t, svcDir, "build", "./...")
		goWork(t, svcDir, "vet", "./...")
		goRun(t, svcDir, "build", "./...") // standalone, through the replace
	}
}

// goWork runs the go tool in dir in workspace mode, using the scaffolded
// go.work rather than any GOWORK set in the environment.
func goWork(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go %s in %s (workspace): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}
