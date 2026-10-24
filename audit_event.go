package bosun

import (
	"context"
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// --- audit ---

// AuditEvent is emitted for every typed request when an Auditor is registered.
type AuditEvent struct {
	Time       time.Time     `json:"time"`
	Method     string        `json:"method"`
	Path       string        `json:"path"`
	Route      string        `json:"route"`
	Status     int           `json:"status"`
	RemoteAddr string        `json:"remote_addr"`
	Duration   time.Duration `json:"duration"`
	Request    any           `json:"request,omitempty"`  // redacted
	Response   any           `json:"response,omitempty"` // redacted
	Err        string        `json:"error,omitempty"`
	ErrOrigin  string        `json:"error_origin,omitempty"`
	// Correlation is the request's correlation id (see mw.Correlation), empty
	// when no correlation middleware ran.
	Correlation string `json:"correlation,omitempty"`
}

// Auditor receives audit events. Enable auditing by registering an
// implementation (e.g. import the bosun/audit module).
type Auditor interface {
	Audit(ctx context.Context, ev AuditEvent)
}

// sensitive field names that are always redacted, case-insensitive.
var sensitiveNames = []string{"password", "secret", "token", "apikey", "api_key", "authorization"}

// Redact converts v into a JSON-friendly map/value with sensitive fields
// replaced by "[REDACTED]". Fields are sensitive if tagged `audit:"-"` or if
// their (json) name contains a well-known sensitive word. Fields tagged
// `json:"-"` are left out. Values that marshal themselves (such as time.Time)
// are rendered through encoding.TextMarshaler or json.Marshaler, cycles are
// cut and nesting stops at redactMaxDepth.
func Redact(v any) any {
	r := redactor{seen: map[uintptr]bool{}}
	return r.value(reflect.ValueOf(v), 0)
}

const redactMaxDepth = 32

var (
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
	jsonMarshalerType = reflect.TypeFor[json.Marshaler]()
)

// redactor tracks the pointers, maps and slices on the current path so a
// back-reference ends the walk instead of overflowing the stack.
type redactor struct {
	seen map[uintptr]bool
}

func (r *redactor) value(v reflect.Value, depth int) any {
	if !v.IsValid() {
		return nil
	}
	if depth > redactMaxDepth {
		return "[MAX DEPTH]"
	}
	if out, ok := marshalSelf(v); ok {
		return out
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice:
		if v.IsNil() {
			return nil
		}
		if v.Kind() != reflect.Interface {
			p := v.Pointer()
			if r.seen[p] {
				return "[CYCLE]"
			}
			r.seen[p] = true
			defer delete(r.seen, p)
		}
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		return r.value(v.Elem(), depth+1)
	case reflect.Struct:
		out := map[string]any{}
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			sf := t.Field(i)
			if !sf.IsExported() {
				continue
			}
			jt := sf.Tag.Get("json")
			if jt == "-" {
				continue
			}
			name := sf.Name
			if n := strings.Split(jt, ",")[0]; n != "" {
				name = n
			}
			if sf.Tag.Get("audit") == "-" || isSensitive(name) {
				out[name] = "[REDACTED]"
				continue
			}
			out[name] = r.value(v.Field(i), depth+1)
		}
		return out
	case reflect.Slice, reflect.Array:
		out := make([]any, v.Len())
		for i := 0; i < v.Len(); i++ {
			out[i] = r.value(v.Index(i), depth+1)
		}
		return out
	case reflect.Map:
		out := map[string]any{}
		for _, k := range v.MapKeys() {
			ks := fmt.Sprint(k.Interface())
			if isSensitive(ks) {
				out[ks] = "[REDACTED]"
				continue
			}
			out[ks] = r.value(v.MapIndex(k), depth+1)
		}
		return out
	default:
		return v.Interface()
	}
}

// marshalSelf renders a value whose type controls its own encoding, as
// encoding/json would. Byte slices are left to the default case.
func marshalSelf(v reflect.Value) (any, bool) {
	t := v.Type()
	if t.Kind() == reflect.Interface || (t.Kind() == reflect.Pointer && v.IsNil()) {
		return nil, false
	}
	switch {
	case t.Implements(textMarshalerType):
		if b, err := v.Interface().(encoding.TextMarshaler).MarshalText(); err == nil {
			return string(b), true
		}
	case t.Implements(jsonMarshalerType):
		if b, err := v.Interface().(json.Marshaler).MarshalJSON(); err == nil && json.Valid(b) {
			return json.RawMessage(b), true
		}
	}
	return nil, false
}

func isSensitive(name string) bool {
	l := strings.ToLower(name)
	for _, s := range sensitiveNames {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}
