// Package healthmod adds liveness/readiness endpoints to any bosun app.
//
// Liveness (/health/live) reports that the process is up. Readiness
// (/health/ready) runs every registered Probe — database pings, broker
// connectivity, downstream checks — and returns 503 with the failing probe
// names until they all pass, so an orchestrator (Docker/Caddy) can gate traffic.
//
// Register a probe from any module or from main:
//
//	var _ = healthmod.Register(healthmod.ProbeFunc{N: "db", F: func(ctx context.Context) error {
//	    return db.PingContext(ctx)
//	}})
//
// Or register a probe resolved from the DI container:
//
//	var _ = healthmod.RegisterInjected[*MyProbe]()
package healthmod

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/amberstack/bosun"
	"github.com/amberstack/bosun/registry"
)

// Probe is a readiness check contributed to /health/ready.
type Probe interface {
	Name() string
	Check(ctx context.Context) error
}

// ProbeFunc adapts a named function to Probe.
type ProbeFunc struct {
	N string
	F func(context.Context) error
}

func (p ProbeFunc) Name() string                    { return p.N }
func (p ProbeFunc) Check(ctx context.Context) error { return p.F(ctx) }

var _ Probe = ProbeFunc{}

// ReadyTimeout bounds how long the readiness handler waits for all probes.
var ReadyTimeout = 3 * time.Second

var (
	probeMu      sync.Mutex
	probes       []Probe
	pendingTypes []func(*registry.Registry) (Probe, error)
)

// Register adds a ready-made probe (mirrors config.Add).
func Register(p Probe) struct{} {
	probeMu.Lock()
	probes = append(probes, p)
	probeMu.Unlock()
	return struct{}{}
}

// RegisterInjected adds a probe resolved from the DI container, for probes with
// injected dependencies declared via bosun.Service[T]() (mirrors config.AddSource).
func RegisterInjected[T any]() struct{} {
	probeMu.Lock()
	pendingTypes = append(pendingTypes, func(reg *registry.Registry) (Probe, error) {
		v, err := registry.Resolve[T](reg)
		if err != nil {
			return nil, err
		}
		p, ok := any(v).(Probe)
		if !ok {
			return nil, fmt.Errorf("healthmod: %T does not implement healthmod.Probe", v)
		}
		return p, nil
	})
	probeMu.Unlock()
	return struct{}{}
}

// HealthController mounts /health/live and /health/ready.
type HealthController struct {
	Reg      *registry.Registry // injected
	resolved []Probe
}

var _ = bosun.Controller[HealthController]("/health")

// Init resolves injected probes once the DI graph is complete.
func (c *HealthController) Init() error {
	probeMu.Lock()
	defer probeMu.Unlock()
	for _, f := range pendingTypes {
		p, err := f(c.Reg)
		if err != nil {
			return err
		}
		c.resolved = append(c.resolved, p)
	}
	return nil
}

func (c *HealthController) Routes(r *bosun.Router) {
	r.Get("/live", c.Live)
	r.Get("/ready", c.Ready)
}

// Live reports process liveness.
func (c *HealthController) Live(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// Ready runs every registered probe concurrently and reports readiness.
func (c *HealthController) Ready(w http.ResponseWriter, r *http.Request) {
	probeMu.Lock()
	all := make([]Probe, 0, len(probes)+len(c.resolved))
	all = append(all, probes...)
	all = append(all, c.resolved...)
	probeMu.Unlock()

	ctx, cancel := context.WithTimeout(r.Context(), ReadyTimeout)
	defer cancel()

	var (
		mu     sync.Mutex
		failed = map[string]string{}
		wg     sync.WaitGroup
	)
	for _, p := range all {
		wg.Add(1)
		go func(p Probe) {
			defer wg.Done()
			if err := p.Check(ctx); err != nil {
				mu.Lock()
				failed[p.Name()] = err.Error()
				mu.Unlock()
			}
		}(p)
	}
	wg.Wait()

	if len(failed) > 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unready", "failed": failed})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
