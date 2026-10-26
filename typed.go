package bosun

// Typed route registration and the request adapter: binds the request body
// into Req[In].Body, invokes the handler, writes the response, records the
// observed status, and emits the audit event. The raw *http.Request is
// always available on Req[In] via embedding.

import (
	"context"
	"encoding/json"
	"fmt"
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
// skips parsing, and any decodes loose JSON. In must not be a pointer;
// mounting Req[*T] fails app.Start.
//
// Out controls response encoding: structs/maps/etc are JSON, string is
// text/plain, []byte is application/octet-stream (json.RawMessage, or any
// byte slice that implements json.Marshaler, is JSON), struct{} writes status
// only with no body. A value that fails to encode becomes a 500.
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
	routeMaxBody, hasRouteMaxBody := int64(0), false
	for _, o := range opts {
		switch v := o.(type) {
		case MWRef:
			mws = append(mws, v)
		case declaredErrors:
			declared = append(declared, v...)
		case maxBody:
			routeMaxBody, hasRouteMaxBody = int64(v), true
		}
	}

	p = normalizePath(p)
	full := r.fullPath(p)
	paramNames := extractParamNames(full)
	handlerName := runtime.FuncForPC(reflect.ValueOf(h).Pointer()).Name()

	inType := reflect.TypeOf((*In)(nil)).Elem()
	if inType.Kind() == reflect.Pointer {
		// Binding and validation fill a value, not a pointer to one; a pointer
		// In would skip both and could leave Body nil.
		*r.errs = append(*r.errs, fmt.Errorf("%s %s: In type %s must not be a pointer; use Req[%s]", method, p, inType, inType.Elem()))
		return
	}
	isString := inType.Kind() == reflect.String
	isEmptyStruct := inType.Kind() == reflect.Struct && inType.NumField() == 0

	// Compile binding and validate tags now so a field bind can't fill or a
	// malformed rule fails app.Start, not requests.
	validates := inType.Kind() == reflect.Struct && !isEmptyStruct
	if validates {
		if _, err := bindPlanFor(inType); err != nil {
			*r.errs = append(*r.errs, fmt.Errorf("%s %s: invalid binding tag: %w", method, p, err))
			return
		}
		if _, err := planFor(inType); err != nil {
			*r.errs = append(*r.errs, fmt.Errorf("%s %s: invalid validate tag: %w", method, p, err))
			return
		}
	}

	outType := reflect.TypeOf((*Out)(nil)).Elem()
	outIsString := outType.Kind() == reflect.String
	outIsBytes := outType.Kind() == reflect.Slice && outType.Elem().Kind() == reflect.Uint8 &&
		!outType.Implements(reflect.TypeFor[json.Marshaler]())
	outIsEmptyStruct := outType.Kind() == reflect.Struct && outType.NumField() == 0

	// The app limit covers bodies the framework parses; a struct{} In leaves
	// the body to the handler (e.g. streaming uploads), so only an explicit
	// MaxBody caps it.
	bodyLimit := r.app.maxBody
	if isEmptyStruct {
		bodyLimit = 0
	}
	if hasRouteMaxBody {
		bodyLimit = routeMaxBody
	}

	recordRoute(r.app, RouteInfo{
		Method:   method,
		Path:     full,
		Handler:  handlerName,
		In:       inType,
		Out:      outType,
		Declared: declared,
	})

	app := r.app
	observed := statusesFor(app, method, full) // resolved now: no lookup or lock per request
	var auditorOnce sync.Once
	var auditor Auditor
	var auditFilter AuditFilter

	adapter := func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()

		auditorOnce.Do(func() {
			t := reflect.TypeOf((*Auditor)(nil)).Elem()
			if app.Reg.Has(t) {
				if v, err := app.Reg.ResolveType(t); err == nil {
					auditor = v.(Auditor)
					auditFilter, _ = auditor.(AuditFilter)
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

		if bodyLimit > 0 && req.Body != nil {
			if req.ContentLength > bodyLimit {
				// Declared too large: refuse without reading any of it.
				bindErr = errBodyTooLarge(fmt.Errorf("content length %d exceeds %d", req.ContentLength, bodyLimit))
			}
			req.Body = http.MaxBytesReader(w, req.Body, bodyLimit)
		}

		switch {
		case bindErr != nil:
			// rejected above
		case isEmptyStruct:
			// nothing to parse
		case isString:
			if req.Body != nil {
				b, err := io.ReadAll(req.Body)
				if bodyTooLarge(err) {
					bindErr = errBodyTooLarge(err)
				} else if err != nil {
					bindErr = err
				} else {
					reflect.ValueOf(&typedReq.Body).Elem().SetString(string(b))
				}
			}
		default:
			bindErr = bind(req, &typedReq.Body)
			if bindErr == nil && validates {
				bindErr = validateInput(&typedReq.Body)
			}
		}

		if bindErr != nil {
			var body any
			status, body = inputErrorBody(bindErr)
			handlerErr = bindErr
			writeJSON(w, status, body)
		} else {
			out, handlerErr = h(req.Context(), typedReq)
			if handlerErr != nil {
				status = errStatus(handlerErr)
				writeJSON(w, status, map[string]string{"error": publicMessage(handlerErr)})
			} else if err := writeOut(w, status, out, outIsString, outIsBytes, outIsEmptyStruct); err != nil {
				status = http.StatusInternalServerError
				handlerErr = fmt.Errorf("encoding response: %w", err)
			}
		}

		observed.add(status)

		if auditor != nil {
			ev := AuditEvent{
				Time:        start,
				Method:      method,
				Path:        full,
				Route:       handlerName,
				Status:      status,
				RemoteAddr:  req.RemoteAddr,
				Duration:    time.Since(start),
				Correlation: CorrelationID(req.Context()),
			}
			if handlerErr != nil {
				ev.Err = handlerErr.Error()
				ev.ErrOrigin = errOrigin(handlerErr)
			}
			// Redaction copies the whole request and response, so ask a
			// filtering auditor first and skip the copies for dropped events.
			if auditFilter == nil || auditFilter.ShouldAudit(req.Context(), ev) {
				ev.Request = Redact(typedReq.Body)
				if handlerErr == nil {
					ev.Response = Redact(out)
				}
				auditor.Audit(req.Context(), ev)
			}
		}
	}

	r.handle(method, p, adapter, mws)
}

func joinPrefix(prefix, p string) string {
	return strings.TrimSuffix(prefix, "/") + "/" + strings.TrimPrefix(p, "/")
}

// writeJSON encodes v before writing the status line, so a value that can't
// be encoded is sent as a 500 rather than a success with an empty body. The
// encoding error is returned.
func writeJSON(w http.ResponseWriter, status int, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		status = http.StatusInternalServerError
		b, _ = json.Marshal(map[string]string{"error": "internal server error"})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
	return err
}

// writeOut emits the handler's Out value. Symmetric with In:
//
//   - string  → text/plain; charset=utf-8, verbatim bytes
//   - []byte  → application/octet-stream, verbatim bytes (json.RawMessage is JSON)
//   - struct{} → no body, status only
//   - any other type → JSON
//
// Named string and byte-slice types are handled through reflection, so
// `type Raw string` works like string. Only JSON encoding can fail.
func writeOut[Out any](w http.ResponseWriter, status int, v Out, isString, isBytes, isEmptyStruct bool) error {
	switch {
	case isEmptyStruct:
		w.WriteHeader(status)
	case isString:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reflect.ValueOf(&v).Elem().String())
	case isBytes:
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(status)
		_, _ = w.Write(reflect.ValueOf(&v).Elem().Bytes())
	default:
		return writeJSON(w, status, v)
	}
	return nil
}
