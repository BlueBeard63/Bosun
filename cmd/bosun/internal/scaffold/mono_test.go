package scaffold_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bluebeard63/bosun/cmd/bosun/internal/scaffold"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestWriteMono(t *testing.T) {
	dir := t.TempDir()
	if _, err := scaffold.WriteMono(dir, scaffold.Mono{
		Module: "example.com/shop", Name: "shop", Port: 8080, Event: "inmem",
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, rel := range []string{"go.mod", "main.go", "modules.go", "Dockerfile", "README.md", "zz_bosun_registry.go",
		"internal/hello/contract.go", "internal/hello/service.go", "internal/hello/controller.go"} {
		if !exists(filepath.Join(dir, rel)) {
			t.Fatalf("missing %s", rel)
		}
		if strings.HasSuffix(rel, ".go") {
			mustParse(t, filepath.Join(dir, rel))
		}
	}
	for _, rel := range []string{"go.work", "contracts", "services", "internal/api"} {
		if exists(filepath.Join(dir, rel)) {
			t.Fatalf("a monolith should have no %s", rel)
		}
	}
	reg := readFile(t, filepath.Join(dir, "zz_bosun_registry.go"))
	if !strings.Contains(reg, `_ "example.com/shop/internal/hello" // Controller[HelloController], Service[HelloService]`) {
		t.Fatalf("registry missing the hello feature:\n%s", reg)
	}
	if mods := readFile(t, filepath.Join(dir, "modules.go")); !strings.Contains(mods, "eventmemmod.Default()") ||
		!strings.Contains(mods, "modules/healthmod") {
		t.Fatalf("modules.go wiring missing:\n%s", mods)
	}
	if df := readFile(t, filepath.Join(dir, "Dockerfile")); !strings.Contains(df, "COPY . .") {
		t.Fatalf("Dockerfile should build the module on its own:\n%s", df)
	}

	// Same validation and overwrite rules as `new service`.
	if _, err := scaffold.WriteMono(dir, scaffold.Mono{Module: "example.com/shop", Name: "shop", Port: 8080}); !errors.Is(err, scaffold.ErrExists) {
		t.Fatalf("err = %v, want ErrExists", err)
	}
	for _, m := range []scaffold.Mono{
		{Module: "example.com/x", Name: "two words", Port: 8080},
		{Module: "example.com//x", Name: "x", Port: 8080},
		{Module: "example.com/x", Name: "x", Port: 8080, Event: "kafka"},
		{Module: "example.com/x", Name: "x", Port: 70000},
	} {
		d := t.TempDir()
		if _, err := scaffold.WriteMono(d, m); err == nil {
			t.Errorf("%+v: no error", m)
		}
		if entries, _ := os.ReadDir(d); len(entries) != 0 {
			t.Errorf("%+v: wrote files before validating", m)
		}
	}
}

func TestWriteBare(t *testing.T) {
	mono := t.TempDir()
	if _, err := scaffold.WriteMono(mono, scaffold.Mono{
		Module: "example.com/shop", Name: "shop", Port: 8080, Event: "redis", Bare: true,
	}); err != nil {
		t.Fatalf("mono: %v", err)
	}
	if exists(filepath.Join(mono, "internal")) {
		t.Fatal("bare monolith has a demo feature")
	}
	if mods := readFile(t, filepath.Join(mono, "modules.go")); !strings.Contains(mods, "eventredismod.Use()") {
		t.Fatalf("bare monolith lost the event wiring:\n%s", mods)
	}
	if reg := readFile(t, filepath.Join(mono, "zz_bosun_registry.go")); strings.Contains(reg, "\nimport") {
		t.Fatalf("bare registry should import nothing:\n%s", reg)
	}

	svc := t.TempDir()
	if _, err := scaffold.WriteService(svc, scaffold.Service{
		Module: "example.com/billing", Name: "billing", Port: 8080, Event: "inmem", Bare: true,
	}); err != nil {
		t.Fatalf("service: %v", err)
	}
	if exists(filepath.Join(svc, "internal")) {
		t.Fatal("bare service has the demo HelloController")
	}
	mustParse(t, filepath.Join(svc, "modules.go"))
	if mods := readFile(t, filepath.Join(svc, "modules.go")); !strings.Contains(mods, "eventmemmod.Default()") {
		t.Fatalf("bare service lost the event wiring:\n%s", mods)
	}

	proj := t.TempDir()
	if _, err := scaffold.WriteProject(proj, scaffold.Project{
		Module: "example.com/acme", Name: "acme", Service: "api", Port: 8080, Event: "none", Bare: true,
	}); err != nil {
		t.Fatalf("project: %v", err)
	}
	for _, rel := range []string{"go.work", "contracts/go.mod", "services/api/go.mod", "services/api/modules.go"} {
		if !exists(filepath.Join(proj, rel)) {
			t.Fatalf("bare project missing %s", rel)
		}
	}
	if exists(filepath.Join(proj, "services/api/internal")) {
		t.Fatal("bare project service has the demo HelloController")
	}
}

// TestScaffoldedMonoBuilds builds and vets monoliths, full and bare, the
// way `bosun new mono` leaves them (tidied), plus bare services and a bare
// project, against this checkout of bosun.
func TestScaffoldedMonoBuilds(t *testing.T) {
	root := bosunRoot(t)
	for _, tc := range []struct {
		name  string
		event string
		bare  bool
	}{
		{"full-inmem", "inmem", false},
		{"full-nats", "nats", false},
		{"bare-inmem", "inmem", true},
		{"bare-none", "none", true},
	} {
		t.Run("mono-"+tc.name, func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(t.TempDir(), "shop")
			if _, err := scaffold.WriteMono(dir, scaffold.Mono{
				Module: "example.com/shop", Name: "shop", Port: 8080, Event: tc.event, LocalBosun: root, Bare: tc.bare,
			}); err != nil {
				t.Fatalf("write: %v", err)
			}
			if err := scaffold.Tidy(dir); err != nil {
				t.Fatalf("tidy: %v", err)
			}
			goRun(t, dir, "build", "./...")
			goRun(t, dir, "vet", "./...")
		})
	}
	for _, event := range []string{"inmem", "amqp"} {
		t.Run("service-bare-"+event, func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(t.TempDir(), "svc")
			if _, err := scaffold.WriteService(dir, scaffold.Service{
				Module: "example.com/svc", Name: "svc", Port: 8080, Event: event, LocalBosun: root, Bare: true,
			}); err != nil {
				t.Fatalf("write: %v", err)
			}
			if err := scaffold.Tidy(dir); err != nil {
				t.Fatalf("tidy: %v", err)
			}
			goRun(t, dir, "build", "./...")
			goRun(t, dir, "vet", "./...")
		})
	}
	t.Run("project-bare", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "acme")
		if _, err := scaffold.WriteProject(dir, scaffold.Project{
			Module: "example.com/acme", Name: "acme", Service: "api", Port: 8080, Event: "inmem", LocalBosun: root, Bare: true,
		}); err != nil {
			t.Fatalf("write: %v", err)
		}
		svc := filepath.Join(dir, "services", "api")
		if err := scaffold.Tidy(svc); err != nil {
			t.Fatalf("tidy: %v", err)
		}
		goWork(t, svc, "build", "./...")
		goWork(t, svc, "vet", "./...")
		goRun(t, svc, "build", "./...")
	})
}
