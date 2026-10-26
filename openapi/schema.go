package openapi

import (
	"encoding/json"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bluebeard63/bosun"
)

// schemaRef returns a $ref for named struct types (registering the schema),
// or an inline schema otherwise.
func schemaRef(t reflect.Type, schemas *schemaSet) any {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() == reflect.Struct && t != reflect.TypeOf(time.Time{}) {
		if t.Name() == "" {
			// anonymous struct: inline, no $ref possible
			return structSchema(t, schemas)
		}
		name := schemas.nameOf(t)
		if _, done := schemas.defs[name]; !done {
			schemas.defs[name] = nil // reserve to break recursion
			schemas.defs[name] = structSchema(t, schemas)
		}
		return map[string]any{"$ref": "#/components/schemas/" + name}
	}
	return schemaFor(t, schemas)
}

func structSchema(t reflect.Type, schemas *schemaSet) map[string]any {
	props := map[string]any{}
	var required []string
	for _, f := range jsonFields(t) {
		rules := fieldRules(f.sf)
		schema := schemaFor(f.sf.Type, schemas)
		if f.quoted {
			schema = map[string]any{"type": "string"}
		}
		prop := decorate(constrain(schema, f.sf.Type, rules), f.sf, true)
		if doc := fieldDoc(f.owner, f.sf); doc != "" {
			prop = withExtras(prop, map[string]any{"description": doc})
		}
		props[f.name] = prop
		if hasRule(rules, "required") {
			required = append(required, f.name)
		}
	}
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	if k := typeKey(t); k != "" {
		if doc := bosun.DocComment(k); doc != "" {
			out["description"] = doc
		}
	}
	return out
}

// decorate adds what a struct field says about its value beyond the
// type: nullable: true for a pointer (when nullable is set, i.e. for body
// properties, which encoding/json sends as null when nil) and the
// `example` tag. A $ref can carry no siblings in OpenAPI 3.0, so a $ref
// schema is wrapped in allOf first.
func decorate(schema any, sf reflect.StructField, nullable bool) any {
	extra := map[string]any{}
	if nullable && sf.Type.Kind() == reflect.Pointer {
		extra["nullable"] = true
	}
	if ex, ok := sf.Tag.Lookup("example"); ok {
		extra["example"] = exampleValue(ex, schema)
	}
	return withExtras(schema, extra)
}

// withExtras sets extra's keys on schema, wrapping a $ref in allOf.
func withExtras(schema any, extra map[string]any) any {
	m, ok := schema.(map[string]any)
	if !ok || len(extra) == 0 {
		return schema
	}
	if _, ref := m["$ref"]; ref {
		m = map[string]any{"allOf": []any{m}}
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// exampleValue turns an `example` tag into a value of the schema's type:
// the raw text for strings, otherwise the tag parsed as JSON (42, true,
// ["a","b"], {"k":1}), falling back to the raw text when it isn't JSON.
func exampleValue(raw string, schema any) any {
	if m, ok := schema.(map[string]any); ok && m["type"] == "string" {
		return raw
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err == nil {
		return v
	}
	return raw
}

// jsonField is one property of a struct as encoding/json sees it.
type jsonField struct {
	name   string
	sf     reflect.StructField
	owner  reflect.Type // struct that declares sf (an embedded one if promoted)
	index  []int
	tagged bool // name came from the json tag
	quoted bool // ",string" option on a number or bool: sent as a string
}

// jsonFields lists t's body properties following encoding/json's rules:
// unexported and `json:"-"` fields are skipped, an empty tag name falls
// back to the Go name, and untagged embedded structs are flattened, with
// shallower and tagged fields winning name conflicts. Fields bound from
// the path, query or headers are left out.
func jsonFields(t reflect.Type) []jsonField {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	type level struct {
		t     reflect.Type
		index []int
	}
	var fields []jsonField
	depth := map[string]int{} // name -> shallowest depth seen
	visited := map[reflect.Type]bool{}
	next := []level{{t: t}}
	for d := 0; len(next) > 0; d++ {
		current := next
		next = nil
		var found []jsonField
		for _, lv := range current {
			if visited[lv.t] {
				continue
			}
			visited[lv.t] = true
			for i := 0; i < lv.t.NumField(); i++ {
				sf := lv.t.Field(i)
				ft := sf.Type
				if ft.Kind() == reflect.Pointer && ft.Name() == "" {
					ft = ft.Elem()
				}
				if sf.Anonymous {
					if !sf.IsExported() && ft.Kind() != reflect.Struct {
						continue
					}
				} else if !sf.IsExported() {
					continue
				}
				tag := sf.Tag.Get("json")
				if tag == "-" || isParamField(sf) {
					continue
				}
				name, opts, _ := strings.Cut(tag, ",")
				index := append(append([]int(nil), lv.index...), i)
				if name == "" && sf.Anonymous && ft.Kind() == reflect.Struct {
					next = append(next, level{t: ft, index: index})
					continue
				}
				f := jsonField{name: name, sf: sf, owner: lv.t, index: index, tagged: name != ""}
				if name == "" {
					f.name = sf.Name
				}
				if hasOpt(opts, "string") {
					switch ft.Kind() {
					case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
						reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
						reflect.Float32, reflect.Float64:
						f.quoted = true
					}
				}
				found = append(found, f)
			}
		}
		// Within one depth, a name used more than once survives only if
		// exactly one of the uses is tagged; names already taken at a
		// shallower depth are hidden.
		byName := map[string][]jsonField{}
		for _, f := range found {
			byName[f.name] = append(byName[f.name], f)
		}
		for name, fs := range byName {
			if _, shallower := depth[name]; shallower {
				continue
			}
			depth[name] = d
			if len(fs) == 1 {
				fields = append(fields, fs[0])
				continue
			}
			var tagged []jsonField
			for _, f := range fs {
				if f.tagged {
					tagged = append(tagged, f)
				}
			}
			if len(tagged) == 1 {
				fields = append(fields, tagged[0])
			}
		}
	}
	sort.Slice(fields, func(i, j int) bool {
		a, b := fields[i].index, fields[j].index
		for k := 0; k < len(a) && k < len(b); k++ {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return len(a) < len(b)
	})
	return fields
}

func hasOpt(opts, want string) bool {
	for _, o := range strings.Split(opts, ",") {
		if o == want {
			return true
		}
	}
	return false
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
			// On a slice the rule applies to each element, so the enum
			// belongs on items.
			target, et := m, t
			if items, ok := m["items"].(map[string]any); ok && items["$ref"] == nil &&
				(t.Kind() == reflect.Slice || t.Kind() == reflect.Array) {
				target, et = items, t.Elem()
				if et.Kind() == reflect.Pointer {
					et = et.Elem()
				}
			}
			var enum []any
			for _, v := range strings.Fields(r.Param) {
				if et.Kind() == reflect.String {
					enum = append(enum, v)
				} else {
					enum = append(enum, number(v))
				}
			}
			target["enum"] = enum
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

func schemaFor(t reflect.Type, schemas *schemaSet) any {
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
		if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8 {
			// encoding/json sends []byte as a base64 string.
			return map[string]any{"type": "string", "format": "byte"}
		}
		return map[string]any{"type": "array", "items": schemaFor(t.Elem(), schemas)}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": schemaFor(t.Elem(), schemas)}
	case reflect.Struct:
		return schemaRef(t, schemas)
	default:
		return map[string]any{}
	}
}
