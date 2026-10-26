// Package scaffold generates Bosun service and workspace skeletons for the
// `bosun new` command. A service is a single deployable Bosun app; a project is
// a Go workspace holding a shared contracts package and one or more services,
// the layout for a microservice architecture where each service is its own app.
package scaffold

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
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

	// Workspace, when set, is the go.work the service is being added to. The
	// service requires the workspace's contracts module through a replace
	// directive, so it also builds outside the workspace (GOWORK=off, Docker).
	Workspace *Workspace

	// Force allows writing into a directory that already has files in it,
	// overwriting any the scaffold generates.
	Force bool

	// Bare writes only the wiring (go.mod, main.go, modules.go, Dockerfile
	// and the registry) without the demo HelloController.
	Bare bool
}

type serviceData struct {
	Service
	EventImport      string
	EventPkg         string
	EventCall        string // "Default" or "Use"
	EventModule      string // driver module to require; "" when the backend lives in bosun
	BosunVersion     string // github.com/bluebeard63/bosun version to require
	GoVersion        string // go directive of the generated go.mod
	GoImage          string // golang image tag the Dockerfile builds with
	BosunReplace     string // quoted replacement path for bosun, from LocalBosun
	EventReplace     string // quoted replacement path for EventModule, from LocalBosun
	ContractsModule  string // workspace contracts module to require, if any
	ContractsReplace string // quoted path from the service to the contracts module
	ContractsDir     string // contracts directory relative to the workspace root
	WorkspaceDir     string // service directory relative to the workspace root
}

var (
	nameRE    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)
	modElemRE = regexp.MustCompile(`^[A-Za-z0-9_~-]([A-Za-z0-9._~-]*[A-Za-z0-9_~-])?$`)
)

// validName rejects names that can't be used verbatim as a directory, a
// module path element and a Go string literal.
func validName(kind, name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("invalid %s name %q: use letters, digits, '-' and '_', starting with a letter", kind, name)
	}
	return nil
}

// validModule checks the shape of a Go module path: slash-separated elements
// of letters, digits and "._~-", none starting or ending with a dot.
func validModule(mod string) error {
	for _, elem := range strings.Split(mod, "/") {
		if !modElemRE.MatchString(elem) {
			return fmt.Errorf("invalid module path %q", mod)
		}
	}
	return nil
}

func validEvent(event string) error {
	switch event {
	case "", "inmem", "amqp", "nats", "redis", "none":
		return nil
	}
	return fmt.Errorf("invalid event backend %q: want inmem, amqp, nats, redis or none", event)
}

func validPort(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("invalid port %d", port)
	}
	return nil
}

func (s Service) validate() error {
	return errors.Join(validName("service", s.Name), validModule(s.Module), validEvent(s.Event), validPort(s.Port))
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

// placeIn fills the workspace-relative paths for a service written to dir.
func (d *serviceData) placeIn(dir string) error {
	ws := d.Workspace
	if ws == nil {
		return nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(ws.Root, abs)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("%s is not inside workspace %s", abs, ws.Root)
	}
	d.WorkspaceDir = filepath.ToSlash(rel)
	if ws.ContractsModule != "" {
		toContracts, err := filepath.Rel(abs, ws.ContractsDir)
		if err != nil {
			return err
		}
		inWS, err := filepath.Rel(ws.Root, ws.ContractsDir)
		if err != nil {
			return err
		}
		d.ContractsModule = ws.ContractsModule
		d.ContractsReplace = strconv.Quote(filepath.ToSlash(toContracts))
		d.ContractsDir = filepath.ToSlash(inWS)
	}
	return nil
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
//
// It refuses a non-empty dir unless s.Force is set, and validates the name
// and module path before writing anything.
func WriteService(dir string, s Service) ([]string, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	if err := checkFree(dir, s.Force); err != nil {
		return nil, err
	}
	d := s.data()
	if err := d.placeIn(dir); err != nil {
		return nil, err
	}
	files := map[string]string{
		"go.mod":              tmplGoMod,
		"main.go":             tmplMain,
		"internal/api/api.go": tmplAPI,
		"Dockerfile":          tmplDockerfile,
	}
	if s.Bare {
		// No demo package to carry the module imports, so package main does.
		delete(files, "internal/api/api.go")
		files["modules.go"] = tmplModules
	}
	if d.WorkspaceDir != "" {
		files["Dockerfile"] = tmplWorkspaceDockerfile
	}
	return renderWithRegistry(dir, files, d)
}

// renderWithRegistry renders files into dir and then generates its registry.
func renderWithRegistry(dir string, files map[string]string, d any) ([]string, error) {
	written, err := render(dir, files, d)
	if err != nil {
		return written, err
	}
	reg, err := writeRegistry(dir)
	return append(written, reg), err
}

// Mono describes a monolith to scaffold: one module and one binary whose
// code is organised by feature under internal/<feature>/, with no go.work,
// contracts module or services/ split.
type Mono struct {
	Module     string
	Name       string
	Port       int
	Event      string
	LocalBosun string // see Service.LocalBosun
	Force      bool   // see Service.Force
	Bare       bool   // only the wiring, without the demo hello feature
}

// WriteMono renders a monolith into dir/ and returns the written paths:
// go.mod, main.go, modules.go (framework modules and the event backend),
// Dockerfile, README.md, the generated registry and, unless m.Bare, a demo
// feature package internal/hello with its contract types, service and
// controller in separate files.
func WriteMono(dir string, m Mono) ([]string, error) {
	if err := errors.Join(validName("project", m.Name), validModule(m.Module), validEvent(m.Event), validPort(m.Port)); err != nil {
		return nil, err
	}
	if err := checkFree(dir, m.Force); err != nil {
		return nil, err
	}
	files := map[string]string{
		"go.mod":     tmplGoMod,
		"main.go":    tmplMain,
		"modules.go": tmplModules,
		"Dockerfile": tmplDockerfile,
		"README.md":  tmplMonoReadme,
	}
	if !m.Bare {
		files["internal/hello/contract.go"] = tmplHelloContract
		files["internal/hello/service.go"] = tmplHelloService
		files["internal/hello/controller.go"] = tmplHelloController
	}
	s := Service{Module: m.Module, Name: m.Name, Port: m.Port, Event: m.Event, LocalBosun: m.LocalBosun}
	return renderWithRegistry(dir, files, s.data())
}

// ErrExists reports a scaffold target directory that already has files in it.
var ErrExists = errors.New("directory already exists and is not empty")

// checkFree refuses to scaffold into a directory with files in it, so an
// existing service is never silently overwritten.
func checkFree(dir string, force bool) error {
	if force {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("%w: %s", ErrExists, dir)
	}
	return nil
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

// Workspace is a Go workspace (go.work) that services are added to.
type Workspace struct {
	Root            string // absolute directory holding go.work
	Module          string // module path prefix, e.g. example.com/acme; "" if unknown
	ContractsModule string // module path of the shared contracts module; "" if none
	ContractsDir    string // absolute directory of the contracts module
}

// FindWorkspace walks up from dir to the nearest go.work and describes it,
// or returns nil if dir isn't inside a workspace. The module prefix and the
// contracts module come from the modules go.work uses: a module whose path
// ends in /contracts, as `bosun new project` creates, or failing that a
// module under <prefix>/services/.
func FindWorkspace(dir string) (*Workspace, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	for d := abs; ; d = filepath.Dir(d) {
		data, err := os.ReadFile(filepath.Join(d, "go.work"))
		if err == nil {
			return describeWorkspace(d, data), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		if filepath.Dir(d) == d {
			return nil, nil
		}
	}
}

func describeWorkspace(root string, gowork []byte) *Workspace {
	ws := &Workspace{Root: root}
	var servicePrefix string
	for _, use := range workUses(gowork) {
		dir := filepath.Join(root, filepath.FromSlash(use))
		mod := modulePath(filepath.Join(dir, "go.mod"))
		switch {
		case mod == "":
		case path.Base(mod) == "contracts" && ws.ContractsModule == "":
			ws.ContractsModule, ws.ContractsDir = mod, dir
			ws.Module = path.Dir(mod)
		case servicePrefix == "" && strings.Contains(mod, "/services/"):
			servicePrefix = mod[:strings.LastIndex(mod, "/services/")]
		}
	}
	if ws.Module == "" || ws.Module == "." {
		ws.Module = servicePrefix
	}
	return ws
}

// workUses returns the directories listed in go.work's use directives.
func workUses(gowork []byte) []string {
	var uses []string
	inBlock := false
	for _, line := range strings.Split(string(gowork), "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		switch {
		case inBlock && line == ")":
			inBlock = false
		case inBlock && line != "":
			uses = append(uses, unquote(line))
		case line == "use (":
			inBlock = true
		case strings.HasPrefix(line, "use "):
			uses = append(uses, unquote(strings.TrimSpace(strings.TrimPrefix(line, "use "))))
		}
	}
	return uses
}

var moduleRE = regexp.MustCompile(`(?m)^module\s+(\S+)`)

// modulePath reads the module path from a go.mod, or "" if there is none.
func modulePath(gomod string) string {
	data, err := os.ReadFile(gomod)
	if err != nil {
		return ""
	}
	m := moduleRE.FindSubmatch(data)
	if m == nil {
		return ""
	}
	return unquote(string(m[1]))
}

func unquote(s string) string {
	if u, err := strconv.Unquote(s); err == nil {
		return u
	}
	return s
}

// Use adds the module in dir to go.work with `go work use`, which also
// raises go.work's go line to the module's if needed.
func (w *Workspace) Use(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(w.Root, abs)
	if err != nil {
		return err
	}
	return goCmd(w.Root, "work", "use", "./"+filepath.ToSlash(rel))
}

// Project describes a workspace to scaffold, including its first service.
type Project struct {
	Module     string
	Name       string
	Service    string // first service name
	Port       int
	Event      string
	LocalBosun string // see Service.LocalBosun
	Force      bool   // see Service.Force
	Bare       bool   // see Service.Bare
}

// WriteProject renders a workspace into dir/ (go.work, the contracts module,
// README) plus its first service under services/<Service>, and returns the
// written paths. go.work uses both modules, and the service requires the
// contracts module through a replace directive.
func WriteProject(dir string, p Project) ([]string, error) {
	if err := errors.Join(validName("project", p.Name), validModule(p.Module)); err != nil {
		return nil, err
	}
	if err := checkFree(dir, p.Force); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	svc := Service{
		Module: p.Module + "/services/" + p.Service, Name: p.Service, Port: p.Port, Event: p.Event,
		LocalBosun: p.LocalBosun, Force: p.Force, Bare: p.Bare,
		Workspace: &Workspace{
			Root: abs, Module: p.Module,
			ContractsModule: p.Module + "/contracts", ContractsDir: filepath.Join(abs, "contracts"),
		},
	}
	if err := svc.validate(); err != nil {
		return nil, err
	}
	pd := struct {
		Project
		GoVersion string // go.work must be at least every module's go directive
	}{Project: p, GoVersion: svc.data().GoVersion}

	written, err := render(dir, map[string]string{
		"go.work":          tmplGoWork,
		"contracts/go.mod": tmplContractsGoMod,
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
{{- if .ContractsModule}}
	{{.ContractsModule}} v0.0.0
{{- end}}
)
{{- if .ContractsModule}}

replace {{.ContractsModule}} => {{.ContractsReplace}}
{{- end}}
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

// tmplModules blank-imports the framework modules an app uses and selects
// its event backend. Bare scaffolds and monoliths carry it in package main,
// where it is present whatever feature packages exist.
const tmplModules = `package main

// Framework modules this app uses: importing a module registers its
// controllers and services. Add more modules here.
import (
	_ "github.com/bluebeard63/bosun/modules/healthmod"
{{- if .EventImport}}

	"{{.EventImport}}"
{{- end}}
)
{{- if .EventCall}}

// The event bus backend for this app.
var _ = {{.EventPkg}}.{{.EventCall}}()
{{- end}}
`

// tmplHelloContract, tmplHelloService and tmplHelloController are the demo
// feature of a monolith: one package per feature, split by role.
const tmplHelloContract = `package hello

// Request and response types of the hello feature. Keep a feature's types
// here, next to the controller that serves them.

// GreetIn is the request for GET /hello/:name.
type GreetIn struct {
	Name string ` + "`path:\"name\"`" + `
}

// GreetOut is the response body for GET /hello/:name.
type GreetOut struct {
	Message string ` + "`json:\"message\"`" + `
}
`

const tmplHelloService = `// Package hello is a demo feature: its contract types, service and
// controller live together. Replace it with your own features, one package
// each under internal/.
package hello

import "github.com/bluebeard63/bosun"

// HelloService holds the feature's business logic. Inject it anywhere with a
// field of type *HelloService.
type HelloService struct{}

var _ = bosun.Service[HelloService]()

// Greet returns a greeting for name.
func (s *HelloService) Greet(name string) string {
	return "hello, " + name + ", from {{.Name}}"
}
`

const tmplHelloController = `package hello

import (
	"context"

	"github.com/bluebeard63/bosun"
)

// HelloController serves the /hello routes. Bosun injects its Hello field.
type HelloController struct {
	Hello *HelloService
}

var _ = bosun.Controller[HelloController]("/hello")

// Routes mounts the controller's routes under /hello.
func (c *HelloController) Routes(r *bosun.Router) {
	bosun.Get(r, "/:name", c.Greet)
}

// Greet says hello.
func (c *HelloController) Greet(ctx context.Context, req *bosun.Req[GreetIn]) (GreetOut, error) {
	return GreetOut{Message: c.Hello.Greet(req.Body.Name)}, nil
}
`

const tmplMonoReadme = `# {{.Name}}

A Bosun monolith: one Go module and one binary, organised by feature.

- ` + "`main.go`" + ` creates and runs the app; ` + "`zz_bosun_registry.go`" + ` (generated) imports every package that registers with Bosun.
- ` + "`modules.go`" + ` lists the framework modules the app uses and selects its event backend.
- ` + "`internal/<feature>/`" + ` holds one package per feature: its controller, service and contract (request and response) types.

Run it with ` + "`go run .`" + `. After adding a feature package, run ` + "`go generate`" + ` to regenerate ` + "`zz_bosun_registry.go`" + `.
Build an image with ` + "`docker build .`" + `.
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

// tmplWorkspaceDockerfile builds a workspace service from the workspace root,
// so the contracts module its go.mod replaces is inside the build context.
const tmplWorkspaceDockerfile = `# Build from the workspace root: docker build -f {{.WorkspaceDir}}/Dockerfile .
FROM golang:{{.GoImage}} AS build
WORKDIR /src
{{- if .ContractsDir}}
COPY {{.ContractsDir}} ./{{.ContractsDir}}
{{- end}}
COPY {{.WorkspaceDir}} ./{{.WorkspaceDir}}
WORKDIR /src/{{.WorkspaceDir}}
RUN CGO_ENABLED=0 GOWORK=off go build -o /app .

FROM gcr.io/distroless/static
COPY --from=build /app /app
EXPOSE {{.Port}}
ENTRYPOINT ["/app"]
`

const tmplGoWork = `go {{.GoVersion}}

use (
	./contracts
	./services/{{.Service}}
)
`

const tmplContractsGoMod = `module {{.Module}}/contracts

go 1.22
`

const tmplContracts = `// Package contracts holds shared types only: event payloads, request and
// response DTOs, and generated clients. It MUST NOT contain any bosun.Service,
// bosun.Controller, or bosun.Default declarations, so importing a contract never
// pulls another service's registrations into your process.
package contracts
`

const tmplReadme = `# {{.Name}}

A Bosun microservice workspace.

- ` + "`contracts/`" + ` shared types only (event payloads, DTOs, generated clients), its own module.
- ` + "`services/{{.Service}}/`" + ` the first service; run it with ` + "`go run ./services/{{.Service}}`" + `.

Add another service with ` + "`bosun new service <name>`" + ` from anywhere in this
workspace: it is created under ` + "`services/`" + ` and added to ` + "`go.work`" + `.
Build a service image from this directory with
` + "`docker build -f services/<name>/Dockerfile .`" + `.
`
