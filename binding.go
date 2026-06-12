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

// bind fills *In from the request body and from struct tags. Body decoding
// depends on Content-Type: JSON for application/json (or empty), form for
// application/x-www-form-urlencoded and multipart/form-data. Per-field
// sources are selected by the first matching tag in this order:
// `path:"x"`, `query:"x"`, `header:"X-Foo"`, `form:"x"`.
func bind(req *http.Request, in any) error {
	ct := req.Header.Get("Content-Type")
	hasBody := req.Body != nil && req.ContentLength != 0
	isForm := strings.HasPrefix(ct, "application/x-www-form-urlencoded") ||
		strings.HasPrefix(ct, "multipart/form-data")

	if isForm {
		if err := req.ParseForm(); err != nil {
			return fmt.Errorf("invalid form body: %w", err)
		}
	} else if hasBody && (ct == "" || strings.HasPrefix(ct, "application/json")) {
		if err := json.NewDecoder(req.Body).Decode(in); err != nil {
			return fmt.Errorf("invalid JSON body: %w", err)
		}
	}

	v := reflect.ValueOf(in).Elem()
	t := v.Type()
	if t.Kind() != reflect.Struct {
		return nil
	}
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		var raw string
		switch {
		case sf.Tag.Get("path") != "":
			raw = req.PathValue(sf.Tag.Get("path"))
		case sf.Tag.Get("query") != "":
			raw = req.URL.Query().Get(sf.Tag.Get("query"))
		case sf.Tag.Get("header") != "":
			raw = req.Header.Get(sf.Tag.Get("header"))
		case sf.Tag.Get("form") != "":
			raw = req.PostForm.Get(sf.Tag.Get("form"))
		default:
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
