package bosun

// Typed route registration and the request adapter: binds the request,
// invokes the handler, writes the response, records the observed status,
// and emits the audit event.

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"time"
)

func Get[In, Out any](r *Router, p string, h func(context.Context, In) (Out, error), opts ...RouteOpt) {
	typed(r, "GET", p, h, opts)
}
func Post[In, Out any](r *Router, p string, h func(context.Context, In) (Out, error), opts ...RouteOpt) {
	typed(r, "POST", p, h, opts)
}
func Put[In, Out any](r *Router, p string, h func(context.Context, In) (Out, error), opts ...RouteOpt) {
	typed(r, "PUT", p, h, opts)
}
func Delete[In, Out any](r *Router, p string, h func(context.Context, In) (Out, error), opts ...RouteOpt) {
	typed(r, "DELETE", p, h, opts)
}
func Patch[In, Out any](r *Router, p string, h func(context.Context, In) (Out, error), opts ...RouteOpt) {
	typed(r, "PATCH", p, h, opts)
}

func typed[In, Out any](r *Router, method, p string, h func(context.Context, In) (Out, error), opts []RouteOpt) {
	var mws []MWRef
	var declared []int
	for _, o := range opts {
		switch v := o.(type) {
		case MWRef:
			mws = append(mws, v)
		case declaredErrors:
			declared = append(declared, v...)
		}
	}

	full := p
	if r.prefix != "" {
		full = joinPrefix(r.prefix, p)
	}
	handlerName := runtime.FuncForPC(reflect.ValueOf(h).Pointer()).Name()

	routeIndexMu.Lock()
	routeIndex = append(routeIndex, RouteInfo{
		Method:   method,
		Path:     full,
		Handler:  handlerName,
		In:       reflect.TypeOf((*In)(nil)).Elem(),
		Out:      reflect.TypeOf((*Out)(nil)).Elem(),
		Declared: declared,
	})
	routeIndexMu.Unlock()

	app := r.app
	var auditorOnce sync.Once
	var auditor Auditor

	adapter := func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()

		auditorOnce.Do(func() {
			t := reflect.TypeOf((*Auditor)(nil)).Elem()
			if app.Reg.Has(t) {
				if v, err := app.Reg.ResolveType(t); err == nil {
					auditor = v.(Auditor)
				}
			}
		})

		var in In
		status := http.StatusOK
		var out Out
		var handlerErr error

		if err := bind(req, &in); err != nil {
			status = http.StatusBadRequest
			handlerErr = err
			http.Error(w, err.Error(), status)
		} else {
			out, handlerErr = h(req.Context(), in)
			if handlerErr != nil {
				status = errStatus(handlerErr)
				writeJSON(w, status, map[string]string{"error": publicMessage(handlerErr)})
			} else {
				writeJSON(w, status, out)
			}
		}

		recordObserved(method, full, status)

		if auditor != nil {
			ev := AuditEvent{
				Time:       start,
				Method:     method,
				Path:       full,
				Route:      handlerName,
				Status:     status,
				RemoteAddr: req.RemoteAddr,
				Duration:   time.Since(start),
				Request:    Redact(in),
			}
			if handlerErr == nil {
				ev.Response = Redact(out)
			} else {
				ev.Err = handlerErr.Error()
				ev.ErrOrigin = errOrigin(handlerErr)
			}
			auditor.Audit(req.Context(), ev)
		}
	}

	r.handle(method, p, adapter, mws)
}

func joinPrefix(prefix, p string) string {
	return strings.TrimSuffix(prefix, "/") + "/" + strings.TrimPrefix(p, "/")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
