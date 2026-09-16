// Package scaffold generates Bosun service and workspace skeletons for the
// `bosun new` command. A service is a single deployable Bosun app; a project is
// a Go workspace holding a shared contracts package and one or more services,
// the layout for a microservice architecture where each service is its own app.
package scaffold

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"text/template"
)

// Service describes a single service to scaffold.
type Service struct {
	Module string // Go module path
	Name   string // service name
	Port   int
	Event  string // inmem | amqp | nats | redis | none
}

type serviceData struct {
	Service
	EventImport  string
	EventPkg     string
	EventCall    string // "Default" or "Use"
	BosunVersion string // github.com/bluebeard63/bosun version to require
}

func (s Service) data() serviceData {
	d := serviceData{Service: s, BosunVersion: bosunVersion()}
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
	return d
}

// bosunModulePath is the module scaffolded projects depend on.
const bosunModulePath = "github.com/bluebeard63/bosun"

// fallbackBosunVersion pins the bosun version scaffolded go.mod files require
// when the running binary carries no usable build info (e.g. `go run` from
// source or a dev build). Keep in sync with cmd/bosun/go.mod's bosun require.
const fallbackBosunVersion = "v0.6.0"

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
func WriteService(dir string, s Service) ([]string, error) {
	files := map[string]string{
		"go.mod":              tmplGoMod,
		"main.go":             tmplMain,
		"internal/api/api.go": tmplAPI,
		"Dockerfile":          tmplDockerfile,
	}
	return render(dir, files, s.data())
}

// Project describes a workspace to scaffold, including its first service.
type Project struct {
	Module  string
	Name    string
	Service string // first service name
	Port    int
	Event   string
}

// WriteProject renders a workspace into dir/ (go.work, contracts, README) plus
// its first service under services/<Service>, and returns the written paths.
func WriteProject(dir string, p Project) ([]string, error) {
	pd := struct {
		Project
		ServiceModule string
	}{Project: p, ServiceModule: p.Module + "/services/" + p.Service}

	written, err := render(dir, map[string]string{
		"go.work":            tmplGoWork,
		"contracts/doc.go":   tmplContracts,
		"README.md":          tmplReadme,
	}, pd)
	if err != nil {
		return written, err
	}
	svc, err := WriteService(filepath.Join(dir, "services", p.Service), Service{
		Module: pd.ServiceModule, Name: p.Service, Port: p.Port, Event: p.Event,
	})
	return append(written, svc...), err
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

go 1.22

require github.com/bluebeard63/bosun {{.BosunVersion}}
`

const tmplMain = `package main

import (
	"log"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/modules/manifestmod"
	"github.com/bluebeard63/bosun/registry"

	_ "{{.Module}}/internal/api"
)

func main() {
	app := bosun.New()
	registry.RegisterInstance[*manifestmod.Options](app.Reg, &manifestmod.Options{
		Service: "{{.Name}}", Version: "dev", Port: {{.Port}},
	})
	log.Fatal(app.Run(":{{.Port}}"))
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

const tmplDockerfile = `FROM golang:1.22 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /app .

FROM gcr.io/distroless/static
COPY --from=build /app /app
EXPOSE {{.Port}}
ENTRYPOINT ["/app"]
`

const tmplGoWork = `go 1.22

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
