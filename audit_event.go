package bosun

import (
	"context"
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
// their (json) name contains a well-known sensitive word.
func Redact(v any) any {
	return redactValue(reflect.ValueOf(v))
}

func redactValue(v reflect.Value) any {
	if !v.IsValid() {
		return nil
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil
		}
		return redactValue(v.Elem())
	case reflect.Struct:
		out := map[string]any{}
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			sf := t.Field(i)
			if !sf.IsExported() {
				continue
			}
			name := sf.Name
			if jt := sf.Tag.Get("json"); jt != "" && jt != "-" {
				name = strings.Split(jt, ",")[0]
			}
			if sf.Tag.Get("audit") == "-" || isSensitive(name) {
				out[name] = "[REDACTED]"
				continue
			}
			out[name] = redactValue(v.Field(i))
		}
		return out
	case reflect.Slice, reflect.Array:
		out := make([]any, v.Len())
		for i := 0; i < v.Len(); i++ {
			out[i] = redactValue(v.Index(i))
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
			out[ks] = redactValue(v.MapIndex(k))
		}
		return out
	default:
		return v.Interface()
	}
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
