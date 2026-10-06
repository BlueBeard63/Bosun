package bosun

import (
	"encoding/json"
	"errors"
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
			return &ValidationError{Message: "invalid form body"}
		}
	} else if hasBody && (ct == "" || strings.HasPrefix(ct, "application/json")) {
		if err := json.NewDecoder(req.Body).Decode(in); err != nil {
			return jsonBindError(err)
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
			if errors.Is(err, errUnsupportedKind) {
				// A field type bind can't fill is a programming error, not bad input.
				return E(http.StatusInternalServerError, "internal server error", fmt.Errorf("field %s: %w", sf.Name, err))
			}
			name, src := clientName(sf)
			return &ValidationError{Message: "invalid request", Fields: []FieldError{{
				Field: name, In: src, Rule: "type", Message: "must be " + kindNoun(sf.Type),
			}}}
		}
	}
	return nil
}

var errUnsupportedKind = errors.New("unsupported bind kind")

// jsonBindError turns a JSON decode failure into a client-safe error that
// names the offending field when the decoder knows it.
func jsonBindError(err error) error {
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) && te.Field != "" {
		return &ValidationError{Message: "invalid JSON body", Fields: []FieldError{{
			Field: te.Field, In: "body", Rule: "type", Message: "must be " + kindNoun(te.Type),
		}}}
	}
	return &ValidationError{Message: "invalid JSON body"}
}

// kindNoun describes a Go type in client terms for "must be ..." messages.
func kindNoun(t reflect.Type) string {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch {
	case t == timeType:
		return "an RFC 3339 date-time"
	case t.Kind() == reflect.String:
		return "a string"
	case t.Kind() == reflect.Bool:
		return "a boolean"
	case t.Kind() == reflect.Float32, t.Kind() == reflect.Float64:
		return "a number"
	case isNumber(t.Kind()):
		return "an integer"
	case t.Kind() == reflect.Slice, t.Kind() == reflect.Array:
		return "an array"
	default:
		return "an object"
	}
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
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return err
		}
		f.SetUint(n)
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
		return fmt.Errorf("%w %s", errUnsupportedKind, f.Kind())
	}
	return nil
}
