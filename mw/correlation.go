package mw

import (
	"net/http"

	"github.com/amberstack/bosun"
)

// --- Correlation ---

// Correlation ensures every request carries a correlation id. It reuses an
// inbound X-Correlation-ID, else derives one from a W3C traceparent, else mints
// a fresh id; it attaches the id to the request context (bosun.CorrelationID)
// and echoes it on the response. Attach it early (outermost) so downstream
// middleware, handlers, and audit events all see the same id:
//
//	var _ = bosun.Controller[API]("/api", bosun.Use[mw.Correlation]())
type Correlation struct{}

func (m *Correlation) Handle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(bosun.HeaderCorrelationID)
		if id == "" {
			if tp := r.Header.Get(bosun.HeaderTraceparent); tp != "" {
				id = bosun.CorrelationIDFromTraceparent(tp)
			}
		}
		if id == "" {
			id = bosun.NewCorrelationID()
		}
		w.Header().Set(bosun.HeaderCorrelationID, id)
		next.ServeHTTP(w, r.WithContext(bosun.WithCorrelationID(r.Context(), id)))
	})
}

var _ = bosun.Middleware[Correlation]()
