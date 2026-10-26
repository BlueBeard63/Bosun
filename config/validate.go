package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// Validator is implemented by bound option types that check their own
// invariants. Bind calls Validate on every freshly decoded value (value or
// pointer receiver) before swapping it in; a non-nil error rejects the
// update. At startup that is fatal; during a reload the previous value is
// kept and the error is logged.
type Validator interface {
	Validate() error
}

var jsonUnmarshalerT = reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()

// decodeStrict unmarshals raw into v like json.Unmarshal, but rejects object
// members v has no field for (matched case-insensitively, as encoding/json
// does).
func decodeStrict(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("unexpected data after the JSON value")
	}
	return nil
}

// unknownFields appends to out the dotted paths of object members in raw
// that decoding into t would silently drop. Names are matched the way
// encoding/json matches them: exact first, then case-insensitively, with
// embedded struct fields promoted. Shape mismatches are left for the decode
// to report; types with their own UnmarshalJSON/UnmarshalText are not
// inspected.
func unknownFields(t reflect.Type, raw json.RawMessage, path string, out *[]string) {
	t = deref(t)
	if pt := reflect.PointerTo(t); pt.Implements(jsonUnmarshalerT) || pt.Implements(textUnmarshalerT) {
		return
	}
	switch t.Kind() {
	case reflect.Struct:
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) != nil {
			return
		}
		fields := jsonFields(t)
		for _, name := range sortedKeys(obj) {
			p := joinPath(path, name)
			ft, ok := matchField(fields, name)
			if !ok {
				*out = append(*out, p)
				continue
			}
			unknownFields(ft, obj[name], p, out)
		}
	case reflect.Map:
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) != nil {
			return
		}
		for _, k := range sortedKeys(obj) {
			unknownFields(t.Elem(), obj[k], joinPath(path, k), out)
		}
	case reflect.Slice, reflect.Array:
		var arr []json.RawMessage
		if json.Unmarshal(raw, &arr) != nil {
			return
		}
		for i, v := range arr {
			unknownFields(t.Elem(), v, path+"["+strconv.Itoa(i)+"]", out)
		}
	}
}

type jsonField struct {
	name string
	typ  reflect.Type
}

// jsonFields lists the JSON-visible fields of struct type t, direct fields
// before promoted ones.
func jsonFields(t reflect.Type) []jsonField {
	var out []jsonField
	seen := map[reflect.Type]bool{}
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		if seen[t] {
			return
		}
		seen[t] = true
		var embedded []reflect.Type
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := f.Tag.Get("json")
			if tag == "-" {
				continue
			}
			name, _, _ := strings.Cut(tag, ",")
			if f.Anonymous && name == "" && deref(f.Type).Kind() == reflect.Struct {
				embedded = append(embedded, deref(f.Type))
				continue
			}
			if !f.IsExported() {
				continue
			}
			if name == "" {
				name = f.Name
			}
			out = append(out, jsonField{name, f.Type})
		}
		for _, et := range embedded {
			walk(et)
		}
	}
	walk(t)
	return out
}

func matchField(fields []jsonField, name string) (reflect.Type, bool) {
	for _, f := range fields {
		if f.name == name {
			return f.typ, true
		}
	}
	for _, f := range fields {
		if strings.EqualFold(f.name, name) {
			return f.typ, true
		}
	}
	return nil, false
}

func joinPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// describeSource names a source in error messages.
func describeSource(s Source) string {
	switch s := s.(type) {
	case FileSource:
		return "file " + s.Path
	case *FileSource:
		return "file " + s.Path
	case EnvSource, *EnvSource:
		return "env"
	case KVSource, *KVSource:
		return "kv store"
	case fmt.Stringer:
		return s.String()
	default:
		return fmt.Sprintf("%T", s)
	}
}
