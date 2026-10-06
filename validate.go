package bosun

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// --- request validation ---
//
// Typed handlers validate In after every binding source (JSON/form body,
// path, query, header, form tags) has been applied and before the handler
// runs. Rules come from `validate:"..."` struct tags; a Validator method adds
// custom checks. Failures produce a 400 with field-level detail:
//
//	{"error":"validation failed","fields":[
//	  {"field":"title","in":"body","rule":"required","message":"is required"}]}

// FieldError describes one invalid input field.
type FieldError struct {
	// Field is the name the client used: the json/path/query/header/form tag
	// name, with nested fields joined by dots and slice indexes in brackets
	// (e.g. "items[2].sku").
	Field string `json:"field"`
	// In is where the value came from: body, path, query, header or form.
	In string `json:"in,omitempty"`
	// Rule is the failed rule: required, min, max, oneof, type, or a custom name.
	Rule string `json:"rule"`
	// Param is the rule's parameter, e.g. "3" for min=3.
	Param string `json:"param,omitempty"`
	// Message is a short human-readable explanation, safe to show clients.
	Message string `json:"message"`
}

// ValidationError is returned (and rendered as a 400) when request input is
// invalid. Fields are in struct declaration order, so output is deterministic.
type ValidationError struct {
	Message string // defaults to "validation failed"
	Fields  []FieldError
}

func (e *ValidationError) Error() string {
	msg := e.message()
	parts := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		parts[i] = f.Field + " " + f.Message
	}
	if len(parts) == 0 {
		return msg
	}
	return msg + ": " + strings.Join(parts, "; ")
}

func (e *ValidationError) message() string {
	if e.Message == "" {
		return "validation failed"
	}
	return e.Message
}

// Invalid returns a ValidationError for a single field — the convenient way
// to fail a custom Validate method:
//
//	func (in BookingIn) Validate() error {
//	    if !in.End.After(in.Start) {
//	        return bosun.Invalid("end", "must be after start")
//	    }
//	    return nil
//	}
func Invalid(field, message string) *ValidationError {
	return &ValidationError{Fields: []FieldError{{Field: field, Rule: "custom", Message: message}}}
}

// Validator is implemented by request types that need checks beyond tag
// rules (cross-field rules, lookups against a fixed set, etc.). Validate runs
// after binding and after every tag rule has passed. Return a
// *ValidationError (see Invalid) for field-level detail, a *bosun.Error to
// choose another status (e.g. 422), or any other error for a 400 whose
// message is shown to the client.
type Validator interface {
	Validate() error
}

// --- rules ---

// Rule is one parsed validate-tag rule, e.g. {Name: "min", Param: "3"}.
// Exported for tooling such as the openapi package.
type Rule struct {
	Name  string
	Param string
}

// ParseRules parses a validate tag value ("required,min=3,oneof=a b") into
// rules, rejecting unknown rule names and malformed parameters.
func ParseRules(tag string) ([]Rule, error) {
	if strings.TrimSpace(tag) == "" {
		return nil, nil
	}
	var rules []Rule
	for _, part := range strings.Split(tag, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, param, _ := strings.Cut(part, "=")
		r := Rule{Name: strings.TrimSpace(name), Param: strings.TrimSpace(param)}
		switch r.Name {
		case "required", "omitempty":
			if r.Param != "" {
				return nil, fmt.Errorf("rule %s takes no parameter", r.Name)
			}
		case "min", "max":
			if _, err := strconv.ParseFloat(r.Param, 64); err != nil {
				return nil, fmt.Errorf("rule %s needs a numeric parameter, got %q", r.Name, r.Param)
			}
		case "oneof":
			if len(strings.Fields(r.Param)) == 0 {
				return nil, fmt.Errorf("rule oneof needs space-separated values")
			}
		default:
			return nil, fmt.Errorf("unknown validate rule %q", r.Name)
		}
		rules = append(rules, r)
	}
	return rules, nil
}

// --- compiled plans (cached per type) ---

type fieldPlan struct {
	index  int
	name   string // client-facing name
	in     string // body, path, query, header, form
	rules  []Rule
	nested *structPlan // struct, *struct, or []struct / []*struct elements
	slice  bool        // nested applies to slice elements
}

type structPlan struct {
	fields []fieldPlan
}

var plans sync.Map // reflect.Type -> *structPlan or error

var timeType = reflect.TypeOf(time.Time{})

// planFor compiles (and caches) the validation plan for struct type t.
// Malformed validate tags are reported here, at route mount time.
func planFor(t reflect.Type) (*structPlan, error) {
	if v, ok := plans.Load(t); ok {
		if err, isErr := v.(error); isErr {
			return nil, err
		}
		return v.(*structPlan), nil
	}
	p, err := buildPlan(t, map[reflect.Type]*structPlan{})
	if err != nil {
		plans.Store(t, err)
		return nil, err
	}
	plans.Store(t, p)
	return p, nil
}

func buildPlan(t reflect.Type, seen map[reflect.Type]*structPlan) (*structPlan, error) {
	if p, ok := seen[t]; ok {
		return p, nil // recursive type: reuse the plan being built
	}
	p := &structPlan{}
	seen[t] = p
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		rules, err := ParseRules(sf.Tag.Get("validate"))
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", t.Name(), sf.Name, err)
		}
		name, in := clientName(sf)
		fp := fieldPlan{index: i, name: name, in: in, rules: rules}
		if err := checkRuleKinds(sf.Type, rules); err != nil {
			return nil, fmt.Errorf("%s.%s: %w", t.Name(), sf.Name, err)
		}

		et := sf.Type
		if et.Kind() == reflect.Pointer {
			et = et.Elem()
		}
		if et.Kind() == reflect.Slice || et.Kind() == reflect.Array {
			fp.slice = true
			et = et.Elem()
			if et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
		}
		if et.Kind() == reflect.Struct && et != timeType {
			nested, err := buildPlan(et, seen)
			if err != nil {
				return nil, err
			}
			fp.nested = nested
		} else {
			fp.slice = false
		}
		if len(fp.rules) > 0 || fp.nested != nil {
			p.fields = append(p.fields, fp)
		}
	}
	return p, nil
}

// clientName returns the name and source a client uses for field sf, using
// the same tag precedence as binding.
func clientName(sf reflect.StructField) (name, in string) {
	for _, src := range []string{"path", "query", "header", "form"} {
		if v := sf.Tag.Get(src); v != "" {
			return v, src
		}
	}
	if jt := sf.Tag.Get("json"); jt != "" && jt != "-" {
		if n := strings.Split(jt, ",")[0]; n != "" {
			return n, "body"
		}
	}
	return sf.Name, "body"
}

// checkRuleKinds rejects rules that cannot apply to the field's type.
func checkRuleKinds(t reflect.Type, rules []Rule) error {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	for _, r := range rules {
		switch r.Name {
		case "min", "max":
			switch {
			case isNumber(t.Kind()), t.Kind() == reflect.String,
				t.Kind() == reflect.Slice, t.Kind() == reflect.Array, t.Kind() == reflect.Map:
			default:
				return fmt.Errorf("rule %s does not apply to %s", r.Name, t)
			}
		case "oneof":
			if t.Kind() != reflect.String && !isNumber(t.Kind()) {
				return fmt.Errorf("rule oneof does not apply to %s", t)
			}
			if isNumber(t.Kind()) {
				for _, v := range strings.Fields(r.Param) {
					if _, err := strconv.ParseFloat(v, 64); err != nil {
						return fmt.Errorf("rule oneof value %q is not a number", v)
					}
				}
			}
		}
	}
	return nil
}

func isNumber(k reflect.Kind) bool {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

// --- running ---

// validateInput runs tag rules then the Validator method on in (a pointer to
// the bound request struct). It returns nil, a *ValidationError, or the
// error returned by Validate.
func validateInput(in any) error {
	v := reflect.ValueOf(in).Elem()
	if v.Kind() == reflect.Struct {
		plan, err := planFor(v.Type())
		if err != nil {
			return err // unreachable after a successful mount check
		}
		var fields []FieldError
		plan.run(v, "", &fields)
		if len(fields) > 0 {
			return &ValidationError{Fields: fields}
		}
	}
	if val, ok := in.(Validator); ok { // pointer method set covers both receivers
		return val.Validate()
	}
	return nil
}

func (p *structPlan) run(v reflect.Value, prefix string, out *[]FieldError) {
	for _, fp := range p.fields {
		f := v.Field(fp.index)
		name := fp.name
		if prefix != "" {
			name = prefix + "." + fp.name
		}
		if fe, ok := checkField(f, fp, name); !ok {
			*out = append(*out, fe)
			continue // one error per field: first failing rule
		}
		if fp.nested == nil {
			continue
		}
		if f.Kind() == reflect.Pointer {
			if f.IsNil() {
				continue
			}
			f = f.Elem()
		}
		if fp.slice {
			for i := 0; i < f.Len(); i++ {
				ev := f.Index(i)
				if ev.Kind() == reflect.Pointer {
					if ev.IsNil() {
						continue
					}
					ev = ev.Elem()
				}
				fp.nested.run(ev, fmt.Sprintf("%s[%d]", name, i), out)
			}
			continue
		}
		fp.nested.run(f, name, out)
	}
}

// checkField applies fp's rules to f and reports the first failure.
//
//   - A nil pointer is "absent": it fails required and skips other rules.
//   - A zero value fails required; with omitempty it skips other rules;
//     otherwise rules run on it (so path "0" is checked against min=1).
func checkField(f reflect.Value, fp fieldPlan, name string) (FieldError, bool) {
	fail := func(r Rule, msg string) (FieldError, bool) {
		return FieldError{Field: name, In: fp.in, Rule: r.Name, Param: r.Param, Message: msg}, false
	}
	has := func(rule string) (Rule, bool) {
		for _, r := range fp.rules {
			if r.Name == rule {
				return r, true
			}
		}
		return Rule{}, false
	}
	if f.Kind() == reflect.Pointer {
		if f.IsNil() {
			if r, ok := has("required"); ok {
				return fail(r, "is required")
			}
			return FieldError{}, true
		}
		f = f.Elem()
	} else if f.IsZero() {
		if r, ok := has("required"); ok {
			return fail(r, "is required")
		}
		if _, ok := has("omitempty"); ok {
			return FieldError{}, true
		}
	}

	for _, r := range fp.rules {
		switch r.Name {
		case "min", "max":
			limit, _ := strconv.ParseFloat(r.Param, 64)
			got, unit := measure(f)
			if (r.Name == "min" && got < limit) || (r.Name == "max" && got > limit) {
				word := "at least"
				if r.Name == "max" {
					word = "at most"
				}
				return fail(r, fmt.Sprintf("must be %s %s%s", word, r.Param, unit))
			}
		case "oneof":
			opts := strings.Fields(r.Param)
			if !oneOf(f, opts) {
				return fail(r, "must be one of: "+strings.Join(opts, ", "))
			}
		}
	}
	return FieldError{}, true
}

// measure returns the value compared by min/max and the unit for messages.
func measure(f reflect.Value) (float64, string) {
	switch f.Kind() {
	case reflect.String:
		return float64(utf8.RuneCountInString(f.String())), " characters"
	case reflect.Slice, reflect.Array, reflect.Map:
		return float64(f.Len()), " items"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(f.Int()), ""
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(f.Uint()), ""
	default:
		return f.Float(), ""
	}
}

func oneOf(f reflect.Value, opts []string) bool {
	for _, o := range opts {
		if f.Kind() == reflect.String {
			if f.String() == o {
				return true
			}
			continue
		}
		n, _ := strconv.ParseFloat(o, 64)
		got, _ := measure(f)
		if got == n {
			return true
		}
	}
	return false
}

// --- error rendering ---

// inputErrorBody converts a bind/validation failure into its status and
// JSON body. ValidationErrors render with fields; *Error keeps its status
// and public message; other errors render their message with a 400.
func inputErrorBody(err error) (int, any) {
	var ve *ValidationError
	if errors.As(err, &ve) {
		body := map[string]any{"error": ve.message()}
		if len(ve.Fields) > 0 {
			body["fields"] = ve.Fields
		}
		return 400, body
	}
	var be *Error
	if errors.As(err, &be) {
		return be.Status, map[string]string{"error": be.Msg}
	}
	return 400, map[string]string{"error": err.Error()}
}
