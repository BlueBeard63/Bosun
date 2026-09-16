// Package manifestmod produces a deploy manifest: a JSON description of a
// service's routes, health endpoints, required env and secrets, and the event
// subjects it produces or consumes. The AmberStack deploy dashboard and a
// Caddyfile generator consume it. The manifest is assembled from information the
// framework already has: bosun.TypedRoutes, eventmod.Declarations, and any
// registered Contributor.
//
// The runtime endpoint GET /.bosun/manifest serves the live manifest. For static
// generation (in a build step), call EmitIfRequested after app.Start().
package manifestmod

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
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

// Build assembles the manifest from the framework's live introspection. Call it
// after app.Start(), when the route index is complete.
func Build(info Info) Manifest {
	m := Manifest{
		Service: info.Service,
		Version: info.Version,
		Port:    info.Port,
		Health:  HealthAdvert{Live: "/health/live", Ready: "/health/ready"},
	}
	for _, ri := range bosun.TypedRoutes() {
		statuses := append([]int{http.StatusOK}, ri.Declared...)
		ra := RouteAdvert{Method: ri.Method, Path: ri.Path, Operation: ri.Handler, Statuses: statuses}
		if ri.In != nil {
			ra.In = ri.In.String()
			ra.InImport = ri.In.PkgPath()
		}
		if ri.Out != nil {
			ra.Out = ri.Out.String()
			ra.OutImport = ri.Out.PkgPath()
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
}

var _ = bosun.Default[*Options](func() *Options { return &Options{Service: "service", Version: "dev"} })

// ManifestController serves the live manifest at GET /.bosun/manifest.
type ManifestController struct {
	Opts *Options // injected
}

var _ = bosun.Controller[ManifestController]("")

func (c *ManifestController) Routes(r *bosun.Router) {
	r.Get("/.bosun/manifest", c.serve) // raw handler so it does not appear in its own manifest
}

func (c *ManifestController) serve(w http.ResponseWriter, r *http.Request) {
	m := Build(Info{Service: c.Opts.Service, Version: c.Opts.Version, Port: c.Opts.Port})
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(m)
}

// EmitIfRequested prints the manifest to stdout and exits when BOSUN_MANIFEST is
// set. Call it in main after app.Start() so the route index is complete:
//
//	app := bosun.New()
//	if err := app.Start(); err != nil { log.Fatal(err) }
//	manifestmod.EmitIfRequested(manifestmod.Info{Service: "billing", Version: v, Port: 8080})
//	log.Fatal(http.ListenAndServe(":8080", app.Mux))
func EmitIfRequested(info Info) {
	if os.Getenv("BOSUN_MANIFEST") == "" {
		return
	}
	m := Build(info)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(m)
	os.Exit(0)
}
