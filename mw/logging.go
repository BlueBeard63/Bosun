// Package mw ships bosun's built-in middleware. Importing it makes the
// types available; attach them with bosun.Use[mw.Logging]() etc.
package mw

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/amberstack/bosun"
)

// --- Logging ---

type Logging struct {
	log *slog.Logger
}

func (m *Logging) Init() error {
	if m.log == nil {
		m.log = slog.Default()
	}
	return nil
}

func (m *Logging) Handle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		m.log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", time.Since(start).String(),
			"remote", r.RemoteAddr,
		)
	})
}

var _ = bosun.Middleware[Logging]()

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
