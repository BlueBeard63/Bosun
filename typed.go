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
	"net/url"
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
// string (raw body verbatim, useful for non-JSON payloads). Params holds
// the URL path parameters declared in the route pattern (e.g. {id} or
// :id), keyed by name.
type Req[In any] struct {
	*http.Request
	Body   In
	Params Params
}

// Params maps URL path parameter names to their values for the current
// request. Reading an unknown key returns the empty string, like any Go
// map.
//
//	id := req.Params["id"]   // "" if the route has no {id} segment
type Params map[string]string

// Query returns the parsed query parameters of the request. Shorthand for
// req.URL.Query().
//
//	q    := req.Query().Get("q")
//	tags := req.Query()["tag"]   // []string for repeated ?tag=a&tag=b
func (r *Req[In]) Query() url.Values {
	return r.URL.Query()
}

// Get registers a typed GET handler at p. The handler shape is
// func(ctx context.Context, req *Req[In]) (Out, error). Return
// bosun.E(status, msg, cause) for controlled errors.
//
// In controls body parsing: a struct gets JSON/form decode + path/query/
// header/form tag binding, string takes the raw body verbatim, struct{}
// skips parsing, and any decodes loose JSON.
//
// Out controls response encoding: structs/maps/etc are JSON, string is
// text/plain, []byte is application/octet-stream, struct{} writes status
// only with no body.
//
// Use bosun.Get(r, ...) — not r.Get(...), which is the untyped escape hatch.
func Get[In, Out any](r *Router, p string, h func(context.Context, *Req[In]) (Out, error), opts ...RouteOpt) {
	typed(r, "GET", p, h, opts)
}

// Post registers a typed POST handler at p. See Get for the handler shape
// and the rules for In.
func Post[In, Out any](r *Router, p string, h func(context.Context, *Req[In]) (Out, error), opts ...RouteOpt) {
	typed(r, "POST", p, h, opts)
}

// Put registers a typed PUT handler at p. See Get for the handler shape
// and the rules for In.
func Put[In, Out any](r *Router, p string, h func(context.Context, *Req[In]) (Out, error), opts ...RouteOpt) {
	typed(r, "PUT", p, h, opts)
}

// Delete registers a typed DELETE handler at p. See Get for the handler
// shape and the rules for In.
func Delete[In, Out any](r *Router, p string, h func(context.Context, *Req[In]) (Out, error), opts ...RouteOpt) {
	typed(r, "DELETE", p, h, opts)
}

// Patch registers a typed PATCH handler at p. See Get for the handler shape
// and the rules for In.
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

	p = normalizePath(p)
	full := p
	if r.prefix != "" {
		full = joinPrefix(r.prefix, p)
	}
	paramNames := extractParamNames(full)
	handlerName := runtime.FuncForPC(reflect.ValueOf(h).Pointer()).Name()

	inType := reflect.TypeOf((*In)(nil)).Elem()
	isString := inType.Kind() == reflect.String
	isEmptyStruct := inType.Kind() == reflect.Struct && inType.NumField() == 0

	outType := reflect.TypeOf((*Out)(nil)).Elem()
	outIsString := outType.Kind() == reflect.String
	outIsBytes := outType.Kind() == reflect.Slice && outType.Elem().Kind() == reflect.Uint8
	outIsEmptyStruct := outType.Kind() == reflect.Struct && outType.NumField() == 0

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
		if len(paramNames) > 0 {
			typedReq.Params = make(Params, len(paramNames))
			for _, name := range paramNames {
				typedReq.Params[name] = req.PathValue(name)
			}
		}
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
				writeOut(w, status, out, outIsString, outIsBytes, outIsEmptyStruct)
			}
		}

		recordObserved(method, full, status)

		if auditor != nil {
			ev := AuditEvent{
				Time:        start,
				Method:      method,
				Path:        full,
				Route:       handlerName,
				Status:      status,
				RemoteAddr:  req.RemoteAddr,
				Duration:    time.Since(start),
				Request:     Redact(typedReq.Body),
				Correlation: CorrelationID(req.Context()),
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

// writeOut emits the handler's Out value. Symmetric with In:
//
//   - string  → text/plain; charset=utf-8, verbatim bytes
//   - []byte  → application/octet-stream, verbatim bytes
//   - struct{} → no body, status only
//   - any other type → JSON
func writeOut[Out any](w http.ResponseWriter, status int, v Out, isString, isBytes, isEmptyStruct bool) {
	switch {
	case isEmptyStruct:
		w.WriteHeader(status)
	case isString:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, *(any(&v).(*string)))
	case isBytes:
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(status)
		_, _ = w.Write(*(any(&v).(*[]byte)))
	default:
		writeJSON(w, status, v)
	}
}
