package bosun

// Typed route registration and the request adapter: binds the request body
// into Req[In].Body, invokes the handler, writes the response, records the
// observed status, and emits the audit event. The raw *http.Request is
// always available on Req[In] via embedding.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Req wraps the incoming request for typed handlers. The embedded
// *http.Request gives handlers full access to headers, cookies, TLS state,
// the raw body reader, etc. Body holds the parsed request body — a struct
// (JSON or form decoded, with path/query/header/form tag binding) or a
// string (raw body verbatim, useful for non-JSON payloads).
type Req[In any] struct {
	*http.Request
	Body In
}

func Get[In, Out any](r *Router, p string, h func(context.Context, *Req[In]) (Out, error), opts ...RouteOpt) {
	typed(r, "GET", p, h, opts)
}
func Post[In, Out any](r *Router, p string, h func(context.Context, *Req[In]) (Out, error), opts ...RouteOpt) {
	typed(r, "POST", p, h, opts)
}
func Put[In, Out any](r *Router, p string, h func(context.Context, *Req[In]) (Out, error), opts ...RouteOpt) {
	typed(r, "PUT", p, h, opts)
}
func Delete[In, Out any](r *Router, p string, h func(context.Context, *Req[In]) (Out, error), opts ...RouteOpt) {
	typed(r, "DELETE", p, h, opts)
}
func Patch[In, Out any](r *Router, p string, h func(context.Context, *Req[In]) (Out, error), opts ...RouteOpt) {
	typed(r, "PATCH", p, h, opts)
}

func typed[In, Out any](r *Router, method, p string, h func(context.Context, *Req[In]) (Out, error), opts []RouteOpt) {
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

	inType := reflect.TypeOf((*In)(nil)).Elem()
	isString := inType.Kind() == reflect.String
	isEmptyStruct := inType.Kind() == reflect.Struct && inType.NumField() == 0

	routeIndexMu.Lock()
	routeIndex = append(routeIndex, RouteInfo{
		Method:   method,
		Path:     full,
		Handler:  handlerName,
		In:       inType,
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

		typedReq := &Req[In]{Request: req}
		status := http.StatusOK
		var out Out
		var handlerErr error
		var bindErr error

		switch {
		case isEmptyStruct:
			// nothing to parse
		case isString:
			if req.Body != nil {
				b, err := io.ReadAll(req.Body)
				if err != nil {
					bindErr = err
				} else {
					*(any(&typedReq.Body).(*string)) = string(b)
				}
			}
		default:
			bindErr = bind(req, &typedReq.Body)
		}

		if bindErr != nil {
			status = http.StatusBadRequest
			handlerErr = bindErr
			http.Error(w, bindErr.Error(), status)
		} else {
			out, handlerErr = h(req.Context(), typedReq)
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
				Request:    Redact(typedReq.Body),
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
