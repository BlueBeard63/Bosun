package openapi

import (
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/bluebeard63/bosun"
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
	var required []string
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() || isParamField(sf) {
			continue
		}
		name := sf.Name
		if jt := sf.Tag.Get("json"); jt != "" && jt != "-" {
			name = strings.Split(jt, ",")[0]
		}
		rules := fieldRules(sf)
		props[name] = constrain(schemaFor(sf.Type, schemas), sf.Type, rules)
		if hasRule(rules, "required") {
			required = append(required, name)
		}
	}
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

// isParamField reports fields bound from the URL or headers rather than the
// body; they are documented as parameters, not body properties.
func isParamField(sf reflect.StructField) bool {
	return sf.Tag.Get("path") != "" || sf.Tag.Get("query") != "" || sf.Tag.Get("header") != ""
}

// fieldRules returns sf's validate rules. Malformed tags already failed
// app.Start, so a parse error here just means no constraints.
func fieldRules(sf reflect.StructField) []bosun.Rule {
	rules, _ := bosun.ParseRules(sf.Tag.Get("validate"))
	return rules
}

func hasRule(rules []bosun.Rule, name string) bool {
	for _, r := range rules {
		if r.Name == name {
			return true
		}
	}
	return false
}

// constrain adds JSON Schema keywords for validate rules to an inline
// schema. $ref schemas are left alone (OpenAPI 3.0 ignores $ref siblings).
func constrain(schema any, t reflect.Type, rules []bosun.Rule) any {
	m, ok := schema.(map[string]any)
	if !ok || m["$ref"] != nil || len(rules) == 0 {
		return schema
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	for _, r := range rules {
		switch r.Name {
		case "min", "max":
			key := map[string]string{"min": "minimum", "max": "maximum"}[r.Name]
			switch t.Kind() {
			case reflect.String:
				key = map[string]string{"min": "minLength", "max": "maxLength"}[r.Name]
			case reflect.Slice, reflect.Array:
				key = map[string]string{"min": "minItems", "max": "maxItems"}[r.Name]
			case reflect.Map:
				key = map[string]string{"min": "minProperties", "max": "maxProperties"}[r.Name]
			}
			m[key] = number(r.Param)
		case "oneof":
			var enum []any
			for _, v := range strings.Fields(r.Param) {
				if t.Kind() == reflect.String {
					enum = append(enum, v)
				} else {
					enum = append(enum, number(v))
				}
			}
			m["enum"] = enum
		}
	}
	return m
}

// number renders a rule parameter as an integer when it is one.
func number(s string) any {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	f, _ := strconv.ParseFloat(s, 64)
	return f
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
