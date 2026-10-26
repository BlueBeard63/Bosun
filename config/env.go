package config

import (
	"context"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

// EnvSource builds config from environment variables (or any KEY=VALUE list,
// e.g. a parsed .env file). For a bind key "ratelimit", variables prefixed
// RATELIMIT_ are collected and decoded against the bound type's fields:
// RATELIMIT_PERMINUTE=100 (or RATELIMIT_PER_MINUTE=100) sets the PerMinute
// field. Field names match the json tag or Go field name case-insensitively,
// with underscores ignored. A double underscore descends into nested structs:
// DB_POOL__MAX=10 sets Pool.Max under bind key "db".
//
// Values are parsed for the target field's type: strings are taken as-is,
// time.Duration uses time.ParseDuration ("5s"), numbers and bools are parsed
// strictly, encoding.TextUnmarshaler types get the raw string, and other
// types (slices, maps, structs) expect a JSON document. Variables naming no
// field are ignored; a value that doesn't parse is an error naming the
// variable.
type EnvSource struct {
	Environ func() []string // defaults to os.Environ
}

func (e EnvSource) Load(ctx context.Context) (map[string]json.RawMessage, error) {
	environ := e.Environ
	if environ == nil {
		environ = os.Environ
	}
	vars := append([]string(nil), environ()...)
	sort.Strings(vars) // deterministic errors when two variables collide
	out := map[string]json.RawMessage{}
	for _, b := range bindings {
		prefix := strings.ToUpper(b.key) + "_"
		fields := map[string]any{}
		for _, kv := range vars {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || !strings.HasPrefix(k, prefix) {
				continue
			}
			path := strings.Split(strings.TrimPrefix(k, prefix), "__")
			if _, err := setEnv(fields, b.typ, path, v); err != nil {
				return nil, fmt.Errorf("config: env %s: %w", k, err)
			}
		}
		if len(fields) > 0 {
			doc, err := json.Marshal(fields)
			if err != nil {
				return nil, fmt.Errorf("config: env for key %q: %w", b.key, err)
			}
			out[b.key] = doc
		}
	}
	return out, nil
}

// setEnv decodes val for the field at path within t and stores it in node,
// creating nested objects as needed. It reports false when path names no
// field of t.
func setEnv(node map[string]any, t reflect.Type, path []string, val string) (bool, error) {
	t = deref(t)
	var name string
	var ft reflect.Type
	switch t.Kind() {
	case reflect.Struct:
		var ok bool
		if name, ft, ok = lookupField(t, path[0]); !ok {
			return false, nil
		}
	case reflect.Map:
		if t.Key().Kind() != reflect.String || path[0] == "" {
			return false, nil
		}
		name, ft = path[0], t.Elem()
	default:
		return false, nil
	}
	prev, exists := node[name]
	if len(path) == 1 {
		if _, nested := prev.(map[string]any); nested {
			return false, errors.New("conflicts with a nested variable for the same field")
		}
		v, err := decodeEnv(ft, val)
		if err != nil {
			return false, err
		}
		node[name] = v
		return true, nil
	}
	child, ok := prev.(map[string]any)
	if exists && !ok {
		return false, errors.New("conflicts with a variable setting the whole field")
	}
	if child == nil {
		child = map[string]any{}
	}
	set, err := setEnv(child, ft, path[1:], val)
	if set {
		node[name] = child
	}
	return set, err
}

// lookupField finds the struct field env segment seg names, returning its
// JSON name and type. Fields of embedded structs are promoted, as in
// encoding/json.
func lookupField(t reflect.Type, seg string) (string, reflect.Type, bool) {
	want := strings.ReplaceAll(seg, "_", "")
	if want == "" {
		return "", nil, false
	}
	var embedded []reflect.Type
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if tag == "-" {
			continue
		}
		if f.Anonymous && tag == "" && deref(f.Type).Kind() == reflect.Struct {
			embedded = append(embedded, f.Type)
			continue
		}
		if !f.IsExported() {
			continue
		}
		name := tag
		if name == "" {
			name = f.Name
		}
		if strings.EqualFold(strings.ReplaceAll(name, "_", ""), want) ||
			strings.EqualFold(f.Name, want) {
			return name, f.Type, true
		}
	}
	for _, et := range embedded {
		if name, ft, ok := lookupField(deref(et), seg); ok {
			return name, ft, true
		}
	}
	return "", nil, false
}

var (
	durationType      = reflect.TypeOf(time.Duration(0))
	textUnmarshalerT  = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()
	errNotFiniteFloat = errors.New("not a finite number")
)

// decodeEnv parses s as a value of type t and returns it in a form that
// json.Marshal renders as t's JSON encoding.
func decodeEnv(t reflect.Type, s string) (any, error) {
	t = deref(t)
	var v any
	var err error
	switch {
	case t == durationType:
		var d time.Duration
		d, err = time.ParseDuration(s)
		v = int64(d)
	case reflect.PointerTo(t).Implements(textUnmarshalerT):
		v = s
	default:
		switch t.Kind() {
		case reflect.String, reflect.Interface:
			v = s
		case reflect.Bool:
			v, err = strconv.ParseBool(s)
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			v, err = strconv.ParseInt(s, 10, t.Bits())
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			v, err = strconv.ParseUint(s, 10, t.Bits())
		case reflect.Float32, reflect.Float64:
			var f float64
			if f, err = strconv.ParseFloat(s, t.Bits()); err == nil && (math.IsNaN(f) || math.IsInf(f, 0)) {
				err = errNotFiniteFloat
			}
			v = f
		default:
			if !json.Valid([]byte(s)) {
				return nil, fmt.Errorf("want a JSON value for %v", t)
			}
			v = json.RawMessage(s)
		}
	}
	if err != nil {
		return nil, err
	}
	// round-trip through the real type so custom UnmarshalJSON/UnmarshalText
	// and JSON shape mismatches are reported here, against the variable
	doc, err := json.Marshal(v)
	if err == nil {
		err = json.Unmarshal(doc, reflect.New(t).Interface())
	}
	if err != nil {
		return nil, err
	}
	return v, nil
}

func deref(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}
