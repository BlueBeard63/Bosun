package bosun

import (
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
)

// --- request binding ---

// bind fills *In from the request body and from struct tags. Body decoding
// depends on Content-Type: JSON for application/json, any *+json type, or
// none; form for application/x-www-form-urlencoded and multipart/form-data.
// A body of any other type is refused with 415, and one that runs past the
// route's size limit (see WithMaxBody) with 413. Per-field
// sources are selected by the first matching tag in this order:
// `path:"x"`, `query:"x"`, `header:"X-Foo"`, `form:"x"`.
//
// `form:` fields take text values only. Uploaded files of a multipart body
// are not bound; read them from the embedded request with req.FormFile,
// which works because the multipart form has already been parsed.
func bind(req *http.Request, in any) error {
	ct := req.Header.Get("Content-Type")
	hasBody := req.Body != nil && req.ContentLength != 0
	mt, _, mtErr := mime.ParseMediaType(ct) // lower-cased, parameters dropped

	switch {
	case mtErr == nil && (mt == "multipart/form-data" || mt == "application/x-www-form-urlencoded"):
		var err error
		if mt == "multipart/form-data" {
			// ParseForm ignores multipart bodies; this also fills PostForm.
			err = req.ParseMultipartForm(multipartMemory)
		} else {
			err = req.ParseForm()
		}
		if bodyTooLarge(err) {
			return errBodyTooLarge(err)
		}
		if err != nil {
			return &ValidationError{Message: "invalid form body"}
		}
	case !hasBody:
		// nothing to decode
	case ct == "" || (mtErr == nil && (mt == "application/json" || strings.HasSuffix(mt, "+json"))):
		if err := json.NewDecoder(req.Body).Decode(in); err != nil {
			if bodyTooLarge(err) {
				return errBodyTooLarge(err)
			}
			return jsonBindError(err)
		}
	default:
		return E(http.StatusUnsupportedMediaType, "unsupported media type", fmt.Errorf("content type %q", ct))
	}

	v := reflect.ValueOf(in).Elem()
	if v.Kind() != reflect.Struct {
		return nil
	}
	plan, err := bindPlanFor(v.Type())
	if err != nil {
		// A field type bind can't fill is a programming error, not bad input.
		// Mounting rejects it, so only a direct call gets here.
		return E(http.StatusInternalServerError, "internal server error", err)
	}
	var query url.Values
	if plan.query {
		query = req.URL.Query() // parsed once, however many query fields
	}
	for i := range plan.fields {
		bf := &plan.fields[i]
		f := v.Field(bf.index)
		var err error
		switch bf.source {
		case "path":
			err = bf.setOne(f, req.PathValue(bf.key))
		case "query":
			err = bf.set(f, query[bf.key])
		case "header":
			err = bf.set(f, req.Header[bf.header])
		case "form":
			err = bf.set(f, req.PostForm[bf.key])
		}
		if err != nil {
			fe := FieldError{Field: bf.key, In: bf.source, Rule: "type", Message: "must be " + kindNoun(bf.elem)}
			if errors.Is(err, strconv.ErrRange) {
				fe.Rule, fe.Message = "range", "is out of range for "+bf.elem.Kind().String()
			}
			return &ValidationError{Message: "invalid request", Fields: []FieldError{fe}}
		}
	}
	return nil
}

// --- bind plans (cached per type) ---

// bindField is one tag-bound field of an In struct, resolved once per type.
type bindField struct {
	index  int
	source string       // path, query, header or form: the first tag present
	key    string       // the tag value; also the field's client-facing name
	header string       // canonical form of key, for header fields
	elem   reflect.Type // scalar type each raw value is parsed into
	ptr    bool         // field is *elem
	slice  bool         // field is []elem, filled from every value of the key
}

// bindPlan lists the tag-bound fields of an In struct type.
type bindPlan struct {
	fields []bindField
	query  bool // some field reads the query string
}

var bindPlans sync.Map // reflect.Type -> *bindPlan or error

// bindPlanFor compiles (and caches) the tag-binding plan for struct type t.
// A tagged field bind can't fill is reported here, at route mount time.
func bindPlanFor(t reflect.Type) (*bindPlan, error) {
	if v, ok := bindPlans.Load(t); ok {
		if err, isErr := v.(error); isErr {
			return nil, err
		}
		return v.(*bindPlan), nil
	}
	p, err := buildBindPlan(t)
	if err != nil {
		bindPlans.Store(t, err)
		return nil, err
	}
	bindPlans.Store(t, p)
	return p, nil
}

func buildBindPlan(t reflect.Type) (*bindPlan, error) {
	p := &bindPlan{}
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		bf := bindField{index: i}
		for _, src := range []string{"path", "query", "header", "form"} {
			if key := sf.Tag.Get(src); key != "" {
				bf.source, bf.key = src, key
				break
			}
		}
		if bf.source == "" {
			continue
		}
		if !sf.IsExported() {
			return nil, fmt.Errorf("field %s: %s tag on an unexported field", sf.Name, bf.source)
		}
		bf.elem = sf.Type
		switch {
		case bf.elem.Kind() == reflect.Pointer:
			bf.ptr, bf.elem = true, bf.elem.Elem()
		case bf.elem.Kind() == reflect.Slice && bf.source != "path":
			bf.slice, bf.elem = true, bf.elem.Elem()
		}
		if !scalarKind(bf.elem.Kind()) {
			return nil, fmt.Errorf("field %s: %s tag on unsupported type %s", sf.Name, bf.source, sf.Type)
		}
		if bf.source == "header" {
			bf.header = http.CanonicalHeaderKey(bf.key)
		}
		p.query = p.query || bf.source == "query"
		p.fields = append(p.fields, bf)
	}
	return p, nil
}

// scalarKind reports whether setFromString can parse into kind k.
func scalarKind(k reflect.Kind) bool {
	return k == reflect.String || k == reflect.Bool || isNumber(k)
}

// set stores the values of a query, header or form key into field f: all of
// them for a slice field, the first otherwise. Empty values count as absent:
// a slice skips them, and see setOne for the rest.
func (bf *bindField) set(f reflect.Value, raws []string) error {
	if !bf.slice {
		if len(raws) == 0 {
			return nil
		}
		return bf.setOne(f, raws[0])
	}
	n := 0
	for _, raw := range raws {
		if raw != "" {
			n++
		}
	}
	if n == 0 {
		return nil
	}
	s := reflect.MakeSlice(f.Type(), n, n)
	j := 0
	for _, raw := range raws {
		if raw == "" {
			continue
		}
		if err := setFromString(s.Index(j), raw); err != nil {
			return err
		}
		j++
	}
	f.Set(s)
	return nil
}

// setOne parses raw into scalar (or pointer-to-scalar) field f. An empty raw
// is absent: f keeps its zero value, or nil for a pointer.
func (bf *bindField) setOne(f reflect.Value, raw string) error {
	if raw == "" {
		return nil
	}
	if bf.ptr {
		pv := reflect.New(bf.elem)
		if err := setFromString(pv.Elem(), raw); err != nil {
			return err
		}
		f.Set(pv)
		return nil
	}
	return setFromString(f, raw)
}

// multipartMemory is how much of a multipart body is held in memory; file
// parts beyond it spill to temporary files, as with http.Request.FormFile.
const multipartMemory = 32 << 20

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
		n, err := strconv.ParseInt(raw, 10, f.Type().Bits())
		if err != nil {
			return err
		}
		f.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(raw, 10, f.Type().Bits())
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
		fl, err := strconv.ParseFloat(raw, f.Type().Bits())
		if err != nil {
			return err
		}
		f.SetFloat(fl)
	default:
		return fmt.Errorf("unsupported bind kind %s", f.Kind()) // excluded by buildBindPlan
	}
	return nil
}
