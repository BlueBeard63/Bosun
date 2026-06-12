// Package healthmod adds liveness/readiness endpoints to any bosun app.
package healthmod

import (
	"fmt"
	"net/http"

	"github.com/amberstack/bosun"
)

type HealthController struct{}

var _ = bosun.Controller[HealthController]("/health")

func (c *HealthController) Routes(r *bosun.Router) {
	r.Get("/live", c.Live)
	r.Get("/ready", c.Ready)
}

func (c *HealthController) Live(w http.ResponseWriter, r *http.Request)  { fmt.Fprintln(w, "ok") }
func (c *HealthController) Ready(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ready") }
