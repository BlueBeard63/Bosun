// Package audit enables audit logging for all typed routes. Importing the
// package installs a slog-based Auditor; hosts can replace it by registering
// their own bosun.Auditor implementation (e.g. one that writes to a database
// or SIEM).
package audit

import (
	"context"
	"log/slog"

	"github.com/bluebeard63/bosun"
)

// SlogAuditor writes audit events as structured logs.
type SlogAuditor struct {
	log *slog.Logger
}

func (a *SlogAuditor) Init() error {
	if a.log == nil {
		a.log = slog.Default()
	}
	return nil
}

func (a *SlogAuditor) Audit(ctx context.Context, ev bosun.AuditEvent) {
	attrs := []any{
		"method", ev.Method,
		"path", ev.Path,
		"route", ev.Route,
		"status", ev.Status,
		"duration", ev.Duration.String(),
		"remote", ev.RemoteAddr,
		"request", ev.Request,
	}
	if ev.Err != "" {
		attrs = append(attrs, "error", ev.Err, "error_origin", ev.ErrOrigin)
		a.log.Error("audit", attrs...)
		return
	}
	attrs = append(attrs, "response", ev.Response)
	a.log.Info("audit", attrs...)
}

var _ = bosun.Service[SlogAuditor]()
var _ = bosun.DefaultBind[bosun.Auditor, SlogAuditor]()
