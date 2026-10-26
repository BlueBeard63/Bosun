// Package manifestmod produces a deploy manifest: a JSON description of a
// service's routes, health endpoints, required env and secrets, and the event
// subjects it produces or consumes. The AmberStack deploy dashboard and a
// Caddyfile generator consume it. The manifest is assembled from information the
// framework already has: the app's typed routes (app.TypedRoutes),
// eventmod.Declarations, and any registered Contributor.
//
// The runtime endpoint GET /.bosun/manifest serves the live manifest. It lists
// every route, env var name and secret name, so it is off by default: set
// Options.Expose to mount it, and keep it off the public internet (for example
// behind an app middleware or a proxy rule). For static generation (in a build
// step), call EmitIfRequestedFor after app.Start(); that needs no endpoint.
package manifestmod

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/modules/eventmod"
)

// RouteAdvert describes one route in the manifest.
type RouteAdvert struct {
	Method    string `json:"method"`
	Path      string `json:"path"`
	Operation string `json:"operation"`
	Statuses  []int  `json:"statuses,omitempty"`
	// In and Out are the Go types of the request body and response, as
	// "pkg.Type" strings; InImport and OutImport are their import paths.
	// These let `bosun gen client` generate a typed client.
	In        string `json:"in,omitempty"`
	Out       string `json:"out,omitempty"`
	InImport  string `json:"in_import,omitempty"`
	OutImport string `json:"out_import,omitempty"`
	// InType and OutType are In and Out with every named type qualified by
	// its import path, including types nested in slices, maps, pointers and
	// type arguments ("[]example.com/app/contracts.User").
	InType  string `json:"in_type,omitempty"`
	OutType string `json:"out_type,omitempty"`
}

// QueueAdvert describes an event subject the service produces or consumes.
type QueueAdvert struct {
	Subject   string `json:"subject"`
	Direction string `json:"direction"`
	Group     string `json:"group,omitempty"`
}

// EnvReq describes a required environment variable.
type EnvReq struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
}

// SecretRef names a secret the service needs.
type SecretRef struct {
	Name string `json:"name"`
}

// HealthAdvert names the health endpoints.
type HealthAdvert struct {
	Live  string `json:"live"`
	Ready string `json:"ready"`
}

// Manifest is the full deploy description.
type Manifest struct {
	Service string        `json:"service"`
	Version string        `json:"version"`
	Port    int           `json:"port"`
	Routes  []RouteAdvert `json:"routes"`
	Health  HealthAdvert  `json:"health"`
	Env     []EnvReq      `json:"env,omitempty"`
	Secrets []SecretRef   `json:"secrets,omitempty"`
	Queues  []QueueAdvert `json:"queues,omitempty"`
}

// Info identifies the service being described.
type Info struct {
	Service string
	Version string
	Port    int
}

// Contributor adds to the manifest (extra env, secrets, or routes). Register one
// to advertise requirements the framework cannot infer.
type Contributor interface {
	Contribute(*Manifest)
}

// ContributorFunc adapts a plain function to Contributor.
type ContributorFunc func(*Manifest)

// Contribute satisfies Contributor.
func (f ContributorFunc) Contribute(m *Manifest) { f(m) }

var contributors []Contributor

// Register adds a manifest contributor.
func Register(c Contributor) struct{} {
	contributors = append(contributors, c)
	return struct{}{}
}

// BuildFor assembles the manifest for app from the framework's live
// introspection. Call it after app.Start(), when the app's routes are mounted.
func BuildFor(app *bosun.App, info Info) Manifest {
	return build(app.TypedRoutes(), info)
}

// Build assembles the manifest from every typed route mounted by any App in
// the process.
//
// Deprecated: use BuildFor(app, info), which only includes app's routes.
func Build(info Info) Manifest {
	return build(bosun.TypedRoutes(), info) //nolint:staticcheck // deprecated aggregate
}

func build(routes []bosun.RouteInfo, info Info) Manifest {
	m := Manifest{
		Service: info.Service,
		Version: info.Version,
		Port:    info.Port,
		Health:  HealthAdvert{Live: "/health/live", Ready: "/health/ready"},
	}
	for _, ri := range routes {
		statuses := append([]int{http.StatusOK}, ri.Declared...)
		ra := RouteAdvert{Method: ri.Method, Path: ri.Path, Operation: ri.Handler, Statuses: statuses}
		if ri.In != nil {
			ra.In = ri.In.String()
			ra.InImport = ri.In.PkgPath()
			ra.InType = qualifiedType(ri.In)
		}
		if ri.Out != nil {
			ra.Out = ri.Out.String()
			ra.OutImport = ri.Out.PkgPath()
			ra.OutType = qualifiedType(ri.Out)
		}
		m.Routes = append(m.Routes, ra)
	}
	sort.Slice(m.Routes, func(i, j int) bool {
		if m.Routes[i].Path != m.Routes[j].Path {
			return m.Routes[i].Path < m.Routes[j].Path
		}
		return m.Routes[i].Method < m.Routes[j].Method
	})
	for _, d := range eventmod.Declarations() {
		m.Queues = append(m.Queues, QueueAdvert{Subject: d.Subject, Direction: string(d.Direction), Group: d.Group})
	}
	for _, c := range contributors {
		c.Contribute(&m)
	}
	return m
}

// qualifiedType renders t like reflect.Type.String, but with every named type
// qualified by its import path rather than its package name, so a generator
// can import each one.
func qualifiedType(t reflect.Type) string {
	if t.Name() != "" {
		if t.PkgPath() == "" {
			return t.Name() // predeclared
		}
		// A generic instantiation's name already qualifies its type arguments.
		return t.PkgPath() + "." + t.Name()
	}
	switch t.Kind() {
	case reflect.Pointer:
		return "*" + qualifiedType(t.Elem())
	case reflect.Slice:
		return "[]" + qualifiedType(t.Elem())
	case reflect.Array:
		return fmt.Sprintf("[%d]%s", t.Len(), qualifiedType(t.Elem()))
	case reflect.Map:
		return "map[" + qualifiedType(t.Key()) + "]" + qualifiedType(t.Elem())
	case reflect.Struct:
		if t.NumField() == 0 {
			return "struct {}"
		}
		fields := make([]string, t.NumField())
		for i := range fields {
			f := t.Field(i)
			s := qualifiedType(f.Type)
			if !f.Anonymous {
				s = f.Name + " " + s
			}
			if f.Tag != "" {
				s += " " + strconv.Quote(string(f.Tag))
			}
			fields[i] = s
		}
		return "struct { " + strings.Join(fields, "; ") + " }"
	default:
		return t.String() // interfaces, funcs and channels
	}
}

// Caddyfile renders a Caddy site block that reverse-proxies to the service.
func Caddyfile(m Manifest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s %s\n", m.Service, m.Version)
	b.WriteString("{$DOMAIN} {\n")
	fmt.Fprintf(&b, "\treverse_proxy localhost:%d\n", m.Port)
	fmt.Fprintf(&b, "\t# readiness: %s\n", m.Health.Ready)
	b.WriteString("}\n")
	return b.String()
}

// Options configures the manifest controller.
type Options struct {
	Service string
	Version string
	Port    int
	// Expose mounts GET /.bosun/manifest. It is false by default because the
	// manifest describes the service's internals; protect the endpoint when
	// you enable it.
	Expose bool
}

var _ = bosun.Default[*Options](func() *Options { return &Options{Service: "service", Version: "dev"} })

// ManifestController serves the live manifest at GET /.bosun/manifest when
// Options.Expose is set.
type ManifestController struct {
	Opts *Options   // injected
	App  *bosun.App // injected: the app this controller is mounted on
}

var _ = bosun.Controller[ManifestController]("")

func (c *ManifestController) Routes(r *bosun.Router) {
	if !c.Opts.Expose {
		return
	}
	r.Get("/.bosun/manifest", c.serve) // raw handler so it does not appear in its own manifest
}

func (c *ManifestController) serve(w http.ResponseWriter, r *http.Request) {
	m := BuildFor(c.App, Info{Service: c.Opts.Service, Version: c.Opts.Version, Port: c.Opts.Port})
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(m)
}

// EmitIfRequestedFor prints app's manifest to stdout and exits when
// BOSUN_MANIFEST is set. Call it in main after app.Start() so the routes are
// mounted:
//
//	app := bosun.New()
//	if err := app.Start(); err != nil { log.Fatal(err) }
//	manifestmod.EmitIfRequestedFor(app, manifestmod.Info{Service: "billing", Version: v, Port: 8080})
//	log.Fatal(http.ListenAndServe(":8080", app.Handler()))
func EmitIfRequestedFor(app *bosun.App, info Info) {
	emit(BuildFor(app, info))
}

// EmitIfRequested is EmitIfRequestedFor over every app in the process.
//
// Deprecated: use EmitIfRequestedFor(app, info).
func EmitIfRequested(info Info) {
	emit(Build(info)) //nolint:staticcheck // deprecated aggregate
}

func emit(m Manifest) {
	if os.Getenv("BOSUN_MANIFEST") == "" {
		return
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(m)
	os.Exit(0)
}
