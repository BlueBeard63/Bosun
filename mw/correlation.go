package mw

import (
	"net/http"

	"github.com/bluebeard63/bosun"
)

// --- Correlation ---

// Correlation ensures every request carries a correlation id. It reuses an
// inbound X-Correlation-ID, else derives one from a W3C traceparent, else mints
// a fresh id; it attaches the id to the request context (bosun.CorrelationID)
// and echoes it on the response. An inbound id longer than 128 characters or
// containing anything outside [A-Za-z0-9._-] is ignored, since it is echoed
// into responses, logs and audit events. Attach it early (outermost) so
// downstream middleware, handlers, and audit events all see the same id:
//
//	var _ = bosun.Controller[API]("/api", bosun.Use[mw.Correlation]())
type Correlation struct{}

// maxCorrelationIDLen caps an accepted inbound correlation id.
const maxCorrelationIDLen = 128

func (m *Correlation) Handle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(bosun.HeaderCorrelationID)
		if !validCorrelationID(id) {
			id = ""
		}
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

// validCorrelationID reports whether id is 1-128 characters of [A-Za-z0-9._-].
func validCorrelationID(id string) bool {
	if id == "" || len(id) > maxCorrelationIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}
