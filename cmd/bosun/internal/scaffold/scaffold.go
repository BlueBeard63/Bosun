// Package scaffold generates Bosun service and workspace skeletons for the
// `bosun new` command. A service is a single deployable Bosun app; a project is
// a Go workspace holding a shared contracts package and one or more services,
// the layout for a microservice architecture where each service is its own app.
package scaffold

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"text/template"

	"github.com/bluebeard63/bosun/cmd/bosun/internal/registrygen"
)

// Service describes a single service to scaffold.
type Service struct {
	Module string // Go module path
	Name   string // service name
	Port   int
	Event  string // inmem | amqp | nats | redis | none

	// LocalBosun, when set, is a local checkout of the bosun repository: the
	// generated go.mod replaces bosun (and the event driver module) with it.
	// It is for developing Bosun itself, so a scaffold builds against
	// unreleased changes.
	LocalBosun string
}

type serviceData struct {
	Service
	EventImport  string
	EventPkg     string
	EventCall    string // "Default" or "Use"
	EventModule  string // driver module to require; "" when the backend lives in bosun
	BosunVersion string // github.com/bluebeard63/bosun version to require
	GoVersion    string // go directive of the generated go.mod
	GoImage      string // golang image tag the Dockerfile builds with
	BosunReplace string // quoted replacement path for bosun, from LocalBosun
	EventReplace string // quoted replacement path for EventModule, from LocalBosun
}

// minGoVersion is the go directive a fresh service needs. Broker drivers that
// require a newer Go than bosun itself raise it, matching the driver module's
// own go directive, so `go mod tidy` has nothing to bump.
var minGoVersion = map[string]string{
	"nats": "1.25.0",
}

func (s Service) data() serviceData {
	d := serviceData{Service: s, BosunVersion: bosunVersion(), GoVersion: "1.22"}
	switch s.Event {
	case "amqp":
		d.EventImport, d.EventPkg, d.EventCall = "github.com/bluebeard63/bosun/modules/eventamqpmod", "eventamqpmod", "Use"
	case "nats":
		d.EventImport, d.EventPkg, d.EventCall = "github.com/bluebeard63/bosun/modules/eventnatsmod", "eventnatsmod", "Use"
	case "redis":
		d.EventImport, d.EventPkg, d.EventCall = "github.com/bluebeard63/bosun/modules/eventredismod", "eventredismod", "Use"
	case "none":
	default:
		d.EventImport, d.EventPkg, d.EventCall = "github.com/bluebeard63/bosun/modules/eventmemmod", "eventmemmod", "Default"
	}
	if d.EventCall == "Use" {
		// Broker drivers are separate modules, released alongside bosun.
		d.EventModule = d.EventImport
	}
	if v, ok := minGoVersion[s.Event]; ok {
		d.GoVersion = v
	}
	d.GoImage = goImage(d.GoVersion)
	if s.LocalBosun != "" {
		root := filepath.ToSlash(s.LocalBosun)
		d.BosunReplace = strconv.Quote(root)
		if d.EventModule != "" {
			d.EventReplace = strconv.Quote(root + "/modules/" + d.EventPkg)
		}
	}
	return d
}

// goImage maps a go directive ("1.25.0") to the golang image tag ("1.25")
// whose toolchain satisfies it.
func goImage(goVersion string) string {
	parts := strings.SplitN(goVersion, ".", 3)
	if len(parts) < 2 {
		return goVersion
	}
	return parts[0] + "." + parts[1]
}

// bosunModulePath is the module scaffolded projects depend on.
const bosunModulePath = "github.com/bluebeard63/bosun"

// fallbackBosunVersion pins the bosun version scaffolded go.mod files require
// when the running binary carries no usable build info (e.g. `go run` from
// source or a dev build). Keep in sync with cmd/bosun/go.mod's bosun require.
const fallbackBosunVersion = "v0.7.0"

// bosunVersion returns the github.com/bluebeard63/bosun version the bosun binary
// was built against, so scaffolded projects pin the same release the CLI uses
// rather than a placeholder. It falls back to fallbackBosunVersion when build
// info is missing or does not carry a released semver (dev/workspace builds).
func bosunVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return fallbackBosunVersion
	}
	for _, dep := range bi.Deps {
		if dep.Path != bosunModulePath {
			continue
		}
		v := dep.Version
		if dep.Replace != nil {
			v = dep.Replace.Version
		}
		if strings.HasPrefix(v, "v") && !strings.Contains(v, "devel") {
			return v
		}
		break
	}
	return fallbackBosunVersion
}

// WriteService renders a service into dir/ and returns the written file paths.
// main.go carries no hand-written side-effect imports: a generated
// zz_bosun_registry.go (bosun gen registry) blank-imports every package that
// registers with Bosun, and `go generate` keeps it current.
func WriteService(dir string, s Service) ([]string, error) {
	files := map[string]string{
		"go.mod":              tmplGoMod,
		"main.go":             tmplMain,
		"internal/api/api.go": tmplAPI,
		"Dockerfile":          tmplDockerfile,
	}
	written, err := render(dir, files, s.data())
	if err != nil {
		return written, err
	}
	reg, err := writeRegistry(dir)
	return append(written, reg), err
}

// writeRegistry generates the service's zz_bosun_registry.go exactly as
// `bosun gen registry` would, so `bosun gen registry --check` passes on a
// fresh scaffold.
func writeRegistry(dir string) (string, error) {
	mod, err := registrygen.Scan(dir)
	if err != nil {
		return "", err
	}
	target, err := registrygen.TargetFor(mod, dir)
	if err != nil {
		return "", err
	}
	src, err := registrygen.Render(mod, target)
	if err != nil {
		return "", err
	}
	full := filepath.Join(dir, registrygen.FileName)
	return full, os.WriteFile(full, src, 0o644)
}

// ErrNoGo reports that the go command isn't on PATH, so Tidy couldn't run.
var ErrNoGo = errors.New("go command not found on PATH")

// Tidy runs `go mod tidy` in a scaffolded service, which resolves its
// dependencies and writes go.sum, then points the Dockerfile at a golang
// image whose toolchain satisfies the resulting go directive. The official
// images set GOTOOLCHAIN=local, so an older image can't build the service.
func Tidy(dir string) error {
	if err := goCmd(dir, "mod", "tidy"); err != nil {
		return err
	}
	return syncDockerfile(dir)
}

func goCmd(dir string, args ...string) error {
	bin, err := exec.LookPath("go")
	if err != nil {
		return ErrNoGo
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return nil
}

var (
	goDirectiveRE = regexp.MustCompile(`(?m)^go (\S+)`)
	dockerFromRE  = regexp.MustCompile(`(?m)^FROM golang:\S+`)
)

// syncDockerfile rewrites the Dockerfile's build image to match go.mod.
func syncDockerfile(dir string) error {
	mod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return err
	}
	m := goDirectiveRE.FindSubmatch(mod)
	if m == nil {
		return nil
	}
	path := filepath.Join(dir, "Dockerfile")
	df, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	df = dockerFromRE.ReplaceAll(df, []byte("FROM golang:"+goImage(string(m[1]))))
	return os.WriteFile(path, df, 0o644)
}

// Project describes a workspace to scaffold, including its first service.
type Project struct {
	Module     string
	Name       string
	Service    string // first service name
	Port       int
	Event      string
	LocalBosun string // see Service.LocalBosun
}

// WriteProject renders a workspace into dir/ (go.work, contracts, README) plus
// its first service under services/<Service>, and returns the written paths.
func WriteProject(dir string, p Project) ([]string, error) {
	svc := Service{
		Module: p.Module + "/services/" + p.Service, Name: p.Service, Port: p.Port, Event: p.Event,
		LocalBosun: p.LocalBosun,
	}
	pd := struct {
		Project
		GoVersion string // go.work must be at least every module's go directive
	}{Project: p, GoVersion: svc.data().GoVersion}

	written, err := render(dir, map[string]string{
		"go.work":          tmplGoWork,
		"contracts/doc.go": tmplContracts,
		"README.md":        tmplReadme,
	}, pd)
	if err != nil {
		return written, err
	}
	svcFiles, err := WriteService(filepath.Join(dir, "services", p.Service), svc)
	return append(written, svcFiles...), err
}

func render(dir string, files map[string]string, data any) ([]string, error) {
	var written []string
	for rel, tmpl := range files {
		t, err := template.New(rel).Parse(tmpl)
		if err != nil {
			return written, err
		}
		var b strings.Builder
		if err := t.Execute(&b, data); err != nil {
			return written, err
		}
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return written, err
		}
		if err := os.WriteFile(full, []byte(b.String()), 0o644); err != nil {
			return written, err
		}
		written = append(written, full)
	}
	return written, nil
}

const tmplGoMod = `module {{.Module}}

go {{.GoVersion}}

require (
	github.com/bluebeard63/bosun {{.BosunVersion}}
{{- if .EventModule}}
	{{.EventModule}} {{.BosunVersion}}
{{- end}}
)
{{- if .BosunReplace}}

replace github.com/bluebeard63/bosun => {{.BosunReplace}}
{{- end}}
{{- if .EventReplace}}

replace {{.EventModule}} => {{.EventReplace}}
{{- end}}
`

const tmplMain = `package main

// Controllers and services register themselves; zz_bosun_registry.go imports
// every package that does. Regenerate it after adding one:
//
//go:generate bosun gen registry

import (
	"log"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/modules/manifestmod"
	"github.com/bluebeard63/bosun/registry"
)

func main() {
	app := bosun.New()
	// Expose serves GET /.bosun/manifest, which lists every route, env var and
	// secret name. Keep it off the public internet in production.
	registry.RegisterInstance[*manifestmod.Options](app.Reg, &manifestmod.Options{
		Service: "{{.Name}}", Version: "dev", Port: {{.Port}}, Expose: true,
	})
	if err := app.Run(":{{.Port}}"); err != nil {
		log.Fatal(err)
	}
}
`

const tmplAPI = `// Package api holds this service's controllers and services.
package api

import (
	"context"

	"github.com/bluebeard63/bosun"
	_ "github.com/bluebeard63/bosun/modules/healthmod"
	_ "github.com/bluebeard63/bosun/modules/manifestmod"
{{- if .EventImport}}

	"{{.EventImport}}"
{{- end}}
)
{{if .EventCall}}
// The event bus backend for this service.
var _ = {{.EventPkg}}.{{.EventCall}}()
{{end}}
// HelloController is a starter route; replace it with your own.
type HelloController struct{}

var _ = bosun.Controller[HelloController]("")

func (c *HelloController) Routes(r *bosun.Router) {
	bosun.Get(r, "/hello", c.Hello)
}

// HelloOut is the response body for GET /hello.
type HelloOut struct {
	Message string ` + "`json:\"message\"`" + `
}

func (c *HelloController) Hello(ctx context.Context, _ *bosun.Req[struct{}]) (HelloOut, error) {
	return HelloOut{Message: "hello from {{.Name}}"}, nil
}
`

const tmplDockerfile = `FROM golang:{{.GoImage}} AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /app .

FROM gcr.io/distroless/static
COPY --from=build /app /app
EXPOSE {{.Port}}
ENTRYPOINT ["/app"]
`

const tmplGoWork = `go {{.GoVersion}}

use (
	./services/{{.Service}}
)
`

const tmplContracts = `// Package contracts holds shared types only: event payloads, request and
// response DTOs, and generated clients. It MUST NOT contain any bosun.Service,
// bosun.Controller, or bosun.Default declarations, so importing a contract never
// pulls another service's registrations into your process.
package contracts
`

const tmplReadme = `# {{.Name}}

A Bosun microservice workspace.

- ` + "`contracts/`" + ` shared types only (event payloads, DTOs, generated clients).
- ` + "`services/{{.Service}}/`" + ` the first service; run it with ` + "`go run ./services/{{.Service}}`" + `.

Add another service with ` + "`bosun new service <name>`" + `.
`
