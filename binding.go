package bosun

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
)

// --- request binding ---

// bind fills *In from the JSON body (when present) and from `path:"x"` and
// `query:"x"` struct tags.
func bind(req *http.Request, in any) error {
	if req.Body != nil && req.ContentLength != 0 {
		ct := req.Header.Get("Content-Type")
		if ct == "" || strings.HasPrefix(ct, "application/json") {
			if err := json.NewDecoder(req.Body).Decode(in); err != nil {
				return fmt.Errorf("invalid JSON body: %w", err)
			}
		}
	}
	v := reflect.ValueOf(in).Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		var raw string
		if name := sf.Tag.Get("path"); name != "" {
			raw = req.PathValue(name)
		} else if name := sf.Tag.Get("query"); name != "" {
			raw = req.URL.Query().Get(name)
		} else {
			continue
		}
		if raw == "" {
			continue
		}
		if err := setFromString(v.Field(i), raw); err != nil {
			return fmt.Errorf("field %s: %w", sf.Name, err)
		}
	}
	return nil
}

func setFromString(f reflect.Value, raw string) error {
	switch f.Kind() {
	case reflect.String:
		f.SetString(raw)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return err
		}
		f.SetInt(n)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return err
		}
		f.SetBool(b)
	case reflect.Float32, reflect.Float64:
		fl, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return err
		}
		f.SetFloat(fl)
	default:
		return fmt.Errorf("unsupported bind kind %s", f.Kind())
	}
	return nil
}
