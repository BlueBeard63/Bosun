// Package healthmod adds liveness/readiness endpoints to any bosun app.
//
// Liveness (/health/live) reports that the process is up. Readiness
// (/health/ready) runs every registered Probe — database pings, broker
// connectivity, downstream checks — and returns 503 with the failing probe
// names until they all pass, so an orchestrator (Docker/Caddy) can gate traffic.
// The endpoints are unauthenticated, so a failing probe is reported as
// "failed" (or "timeout") rather than with its error text, which can include
// hosts and other internals; set DetailedErrors to include the error text.
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

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/registry"
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

// ReadyTimeout bounds how long the readiness handler waits for all probes. A
// probe still running when it expires is reported as "timeout", even if it
// ignores its context.
var ReadyTimeout = 3 * time.Second

// DetailedErrors reports each failing probe's error text in /health/ready
// instead of a generic "failed". Leave it off when the endpoint is reachable
// by untrusted clients.
var DetailedErrors = false

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

	type result struct {
		i   int
		err error
	}
	// Buffered so probes that finish after the timeout don't block forever.
	results := make(chan result, len(all))
	for i, p := range all {
		go func(i int, p Probe) {
			results <- result{i: i, err: p.Check(ctx)}
		}(i, p)
	}

	failed := map[string]string{}
	done := make([]bool, len(all))
wait:
	for range all {
		select {
		case res := <-results:
			done[res.i] = true
			if res.err != nil {
				msg := "failed"
				if DetailedErrors {
					msg = res.err.Error()
				}
				failed[all[res.i].Name()] = msg
			}
		case <-ctx.Done():
			for i, p := range all {
				if !done[i] {
					failed[p.Name()] = "timeout"
				}
			}
			break wait
		}
	}

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
