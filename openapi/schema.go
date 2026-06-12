package openapi

import (
	"reflect"
	"strings"
	"time"
)

// schemaRef returns a $ref for named struct types (registering the schema),
// or an inline schema otherwise.
func schemaRef(t reflect.Type, schemas map[string]any) any {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() == reflect.Struct && t != reflect.TypeOf(time.Time{}) {
		if t.Name() == "" {
			// anonymous struct: inline, no $ref possible
			return structSchema(t, schemas)
		}
		if _, done := schemas[t.Name()]; !done {
			schemas[t.Name()] = nil // reserve to break recursion
			schemas[t.Name()] = structSchema(t, schemas)
		}
		return map[string]any{"$ref": "#/components/schemas/" + t.Name()}
	}
	return schemaFor(t, schemas)
}

func structSchema(t reflect.Type, schemas map[string]any) map[string]any {
	props := map[string]any{}
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() || sf.Tag.Get("path") != "" || sf.Tag.Get("query") != "" {
			continue
		}
		name := sf.Name
		if jt := sf.Tag.Get("json"); jt != "" && jt != "-" {
			name = strings.Split(jt, ",")[0]
		}
		props[name] = schemaFor(sf.Type, schemas)
	}
	return map[string]any{"type": "object", "properties": props}
}

func schemaFor(t reflect.Type, schemas map[string]any) any {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == reflect.TypeOf(time.Time{}) {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": schemaFor(t.Elem(), schemas)}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": schemaFor(t.Elem(), schemas)}
	case reflect.Struct:
		return schemaRef(t, schemas)
	default:
		return map[string]any{}
	}
}
