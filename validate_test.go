package bosun

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// --- unit: rules ---

type vAddress struct {
	City string `json:"city" validate:"required"`
}

type vItem struct {
	SKU string `json:"sku" validate:"required,min=3"`
}

type vInput struct {
	Name    string            `json:"name" validate:"required,min=2,max=5"`
	Age     int               `json:"age" validate:"omitempty,min=18,max=130"`
	Score   float64           `json:"score" validate:"max=1.5"`
	Role    string            `json:"role" validate:"omitempty,oneof=admin editor"`
	Level   int               `json:"level" validate:"omitempty,oneof=1 2 3"`
	Tags    []string          `json:"tags" validate:"max=2"`
	Meta    map[string]string `json:"meta" validate:"omitempty,min=1"`
	Nick    *string           `json:"nick" validate:"required,min=3"`
	Note    *string           `json:"note" validate:"min=3"`
	Home    vAddress          `json:"home"`
	Work    *vAddress         `json:"work"`
	Items   []vItem           `json:"items" validate:"max=3"`
	When    time.Time         `json:"when" validate:"required"`
	private string            `validate:"required"` //nolint:unused // unexported: ignored
}

func strp(s string) *string { return &s }

func validFixture() vInput {
	return vInput{
		Name: "ann", Age: 30, Score: 1, Role: "admin", Level: 2, Tags: []string{"a"},
		Meta: map[string]string{"k": "v"}, Nick: strp("annie"), Note: nil,
		Home: vAddress{City: "York"}, Items: []vItem{{SKU: "abc"}}, When: time.Now(),
	}
}

func fieldsOf(t *testing.T, err error) []FieldError {
	t.Helper()
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("err = %T %v, want *ValidationError", err, err)
	}
	return ve.Fields
}

func TestValidateAcceptsValidInput(t *testing.T) {
	in := validFixture()
	if err := validateInput(&in); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
}

func TestValidateRules(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*vInput)
		field  string
		rule   string
		msg    string
	}{
		{"required string", func(v *vInput) { v.Name = "" }, "name", "required", "is required"},
		{"min chars", func(v *vInput) { v.Name = "a" }, "name", "min", "must be at least 2 characters"},
		{"max chars counts runes", func(v *vInput) { v.Name = "ééééééé" }, "name", "max", "must be at most 5 characters"},
		{"min number", func(v *vInput) { v.Age = 17 }, "age", "min", "must be at least 18"},
		{"max float", func(v *vInput) { v.Score = 1.6 }, "score", "max", "must be at most 1.5"},
		{"oneof string", func(v *vInput) { v.Role = "root" }, "role", "oneof", "must be one of: admin, editor"},
		{"oneof number", func(v *vInput) { v.Level = 4 }, "level", "oneof", "must be one of: 1, 2, 3"},
		{"max items", func(v *vInput) { v.Tags = []string{"a", "b", "c"} }, "tags", "max", "must be at most 2 items"},
		{"required pointer nil", func(v *vInput) { v.Nick = nil }, "nick", "required", "is required"},
		{"pointer min", func(v *vInput) { v.Nick = strp("ab") }, "nick", "min", "must be at least 3 characters"},
		{"optional pointer set", func(v *vInput) { v.Note = strp("x") }, "note", "min", "must be at least 3 characters"},
		{"nested struct", func(v *vInput) { v.Home.City = "" }, "home.city", "required", "is required"},
		{"nested pointer", func(v *vInput) { v.Work = &vAddress{} }, "work.city", "required", "is required"},
		{"slice element", func(v *vInput) { v.Items = []vItem{{SKU: "abc"}, {SKU: "x"}} }, "items[1].sku", "min", "must be at least 3 characters"},
		{"required time", func(v *vInput) { v.When = time.Time{} }, "when", "required", "is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validFixture()
			tc.mutate(&in)
			fs := fieldsOf(t, validateInput(&in))
			if len(fs) != 1 {
				t.Fatalf("fields = %+v, want exactly one", fs)
			}
			f := fs[0]
			if f.Field != tc.field || f.Rule != tc.rule || f.Message != tc.msg || f.In != "body" {
				t.Fatalf("got %+v, want field=%s rule=%s msg=%q", f, tc.field, tc.rule, tc.msg)
			}
		})
	}
}

func TestValidateZeroValuesWithoutOmitemptyAreChecked(t *testing.T) {
	type in struct {
		N int    `json:"n" validate:"min=1"`
		S string `json:"s" validate:"oneof=a b"`
	}
	fs := fieldsOf(t, validateInput(&in{}))
	if len(fs) != 2 || fs[0].Rule != "min" || fs[1].Rule != "oneof" {
		t.Fatalf("zero values must be checked without omitempty: %+v", fs)
	}
}

func TestValidateOmitemptySkipsZeroValues(t *testing.T) {
	in := validFixture()
	in.Age, in.Score, in.Role, in.Level, in.Tags, in.Meta = 0, 0, "", 0, nil, nil
	if err := validateInput(&in); err != nil {
		t.Fatalf("zero optional fields should pass: %v", err)
	}
}

func TestValidateFieldOrderIsDeterministic(t *testing.T) {
	in := vInput{} // many failures
	for i := 0; i < 20; i++ {
		fs := fieldsOf(t, validateInput(&in))
		var names []string
		for _, f := range fs {
			names = append(names, f.Field)
		}
		if got := strings.Join(names, ","); got != "name,nick,home.city,when" {
			t.Fatalf("order = %s", got)
		}
	}
}

type vCustom struct {
	Start int `json:"start" validate:"required"`
	End   int `json:"end" validate:"required"`
}

func (v vCustom) Validate() error {
	if v.End <= v.Start {
		return Invalid("end", "must be after start")
	}
	return nil
}

type vCustomPtr struct {
	Code string `json:"code"`
}

func (v *vCustomPtr) Validate() error {
	if v.Code == "teapot" {
		return E(http.StatusUnprocessableEntity, "no teapots", nil)
	}
	return nil
}

func TestValidatorInterface(t *testing.T) {
	bad := vCustom{Start: 5, End: 1}
	fs := fieldsOf(t, validateInput(&bad))
	if fs[0].Field != "end" || fs[0].Rule != "custom" {
		t.Fatalf("custom = %+v", fs)
	}
	// Tag rules run first: Validate isn't called while required fields are missing.
	missing := vCustom{Start: 5}
	if fs := fieldsOf(t, validateInput(&missing)); fs[0].Rule != "required" {
		t.Fatalf("tag rules should run before Validate: %+v", fs)
	}
	// Pointer-receiver Validate is found too.
	p := vCustomPtr{Code: "teapot"}
	if err := validateInput(&p); err == nil || errStatus(err) != http.StatusUnprocessableEntity {
		t.Fatalf("pointer Validate = %v", err)
	}
}

func TestParseRulesRejectsMalformedTags(t *testing.T) {
	for _, tag := range []string{"min", "min=abc", "oneof=", "required=1", "email", "max=1,bogus"} {
		if _, err := ParseRules(tag); err == nil {
			t.Errorf("ParseRules(%q) should fail", tag)
		}
	}
	rules, err := ParseRules(" required , min=3,oneof=a b ")
	if err != nil || len(rules) != 3 || rules[2] != (Rule{Name: "oneof", Param: "a b"}) {
		t.Fatalf("rules = %+v, %v", rules, err)
	}
}

func TestPlanRejectsRulesOnWrongKinds(t *testing.T) {
	type badMin struct {
		On bool `validate:"min=1"`
	}
	type badOneof struct {
		N int `validate:"oneof=a b"`
	}
	for _, typ := range []reflect.Type{reflect.TypeOf(badMin{}), reflect.TypeOf(badOneof{})} {
		if _, err := planFor(typ); err == nil {
			t.Errorf("planFor(%v) should fail", typ)
		}
	}
}

// --- end to end through the typed adapter ---

type valIn struct {
	ID     int    `path:"id" validate:"min=1"`
	Page   int    `query:"page" validate:"omitempty,max=100"`
	Tenant string `header:"X-Tenant" validate:"required"`
	Title  string `json:"title" validate:"required,max=10"`
}

type valFormIn struct {
	Email string `form:"email" validate:"required"`
}

type valCtrl struct{}

var _ = Controller[valCtrl]("/val")

func (c *valCtrl) Routes(r *Router) {
	Post(r, "/things/{id}", c.create)
	Post(r, "/form", c.form)
	Post(r, "/custom", c.custom)
}

func (c *valCtrl) create(context.Context, *Req[valIn]) (map[string]bool, error) {
	return map[string]bool{"ok": true}, nil
}

func (c *valCtrl) form(context.Context, *Req[valFormIn]) (map[string]bool, error) {
	return map[string]bool{"ok": true}, nil
}

func (c *valCtrl) custom(context.Context, *Req[vCustom]) (map[string]bool, error) {
	return map[string]bool{"ok": true}, nil
}

type errBody struct {
	Error  string       `json:"error"`
	Fields []FieldError `json:"fields"`
}

func send(t *testing.T, app *App, method, target, ct, body string, hdr map[string]string) (int, errBody) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	var eb errBody
	if rec.Code >= 400 {
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Fatalf("error content-type = %q, body %s", ct, rec.Body)
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &eb); err != nil {
			t.Fatalf("error body not JSON: %s", rec.Body)
		}
	}
	return rec.Code, eb
}

func TestValidationAcrossSources(t *testing.T) {
	app := newStarted(t)
	ok := map[string]string{"X-Tenant": "acme"}
	const js = "application/json"

	if code, eb := send(t, app, "POST", "/val/things/1?page=2", js, `{"title":"hi"}`, ok); code != 200 {
		t.Fatalf("valid request: %d %+v", code, eb)
	}

	cases := []struct {
		name, target, body string
		hdr                map[string]string
		want               FieldError
	}{
		{"path", "/val/things/0", `{"title":"hi"}`, nil,
			FieldError{Field: "id", In: "path", Rule: "min", Param: "1", Message: "must be at least 1"}},
		{"query", "/val/things/1?page=101", `{"title":"hi"}`, nil,
			FieldError{Field: "page", In: "query", Rule: "max", Param: "100", Message: "must be at most 100"}},
		{"header", "/val/things/1", `{"title":"hi"}`, map[string]string{},
			FieldError{Field: "X-Tenant", In: "header", Rule: "required", Message: "is required"}},
		{"json", "/val/things/1", `{"title":"far too long title"}`, nil,
			FieldError{Field: "title", In: "body", Rule: "max", Param: "10", Message: "must be at most 10 characters"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hdr := tc.hdr
			if hdr == nil {
				hdr = ok
			}
			code, eb := send(t, app, "POST", tc.target, js, tc.body, hdr)
			if code != http.StatusBadRequest || eb.Error != "validation failed" {
				t.Fatalf("status %d body %+v", code, eb)
			}
			if len(eb.Fields) != 1 || eb.Fields[0] != tc.want {
				t.Fatalf("fields = %+v, want %+v", eb.Fields, tc.want)
			}
		})
	}

	// Path, query and header all fail at once: every field reported, in order.
	code, eb := send(t, app, "POST", "/val/things/0?page=500", js, `{}`, map[string]string{})
	var got []string
	for _, f := range eb.Fields {
		got = append(got, f.Field)
	}
	if code != 400 || strings.Join(got, ",") != "id,page,X-Tenant,title" {
		t.Fatalf("multi: %d %v", code, got)
	}
}

func TestValidationForm(t *testing.T) {
	app := newStarted(t)
	code, eb := send(t, app, "POST", "/val/form", "application/x-www-form-urlencoded", "other=1", nil)
	if code != 400 || eb.Fields[0] != (FieldError{Field: "email", In: "form", Rule: "required", Message: "is required"}) {
		t.Fatalf("form: %d %+v", code, eb)
	}
	if code, _ := send(t, app, "POST", "/val/form", "application/x-www-form-urlencoded", "email=a@b.c", nil); code != 200 {
		t.Fatalf("valid form: %d", code)
	}
}

func TestValidationCustomEndToEnd(t *testing.T) {
	app := newStarted(t)
	code, eb := send(t, app, "POST", "/val/custom", "application/json", `{"start":5,"end":2}`, nil)
	if code != 400 || len(eb.Fields) != 1 || eb.Fields[0].Field != "end" || eb.Fields[0].Message != "must be after start" {
		t.Fatalf("custom: %d %+v", code, eb)
	}
}

func TestBindErrorsAreJSON(t *testing.T) {
	app := newStarted(t)
	ok := map[string]string{"X-Tenant": "acme"}
	const js = "application/json"

	code, eb := send(t, app, "POST", "/val/things/abc", js, `{"title":"hi"}`, ok)
	if code != 400 || eb.Error != "invalid request" ||
		eb.Fields[0] != (FieldError{Field: "id", In: "path", Rule: "type", Message: "must be an integer"}) {
		t.Fatalf("path type: %d %+v", code, eb)
	}

	code, eb = send(t, app, "POST", "/val/things/1", js, `{"title":123}`, ok)
	if code != 400 || eb.Error != "invalid JSON body" ||
		eb.Fields[0] != (FieldError{Field: "title", In: "body", Rule: "type", Message: "must be a string"}) {
		t.Fatalf("json type: %d %+v", code, eb)
	}

	code, eb = send(t, app, "POST", "/val/things/1", js, `{"title":`, ok)
	if code != 400 || eb.Error != "invalid JSON body" || len(eb.Fields) != 0 {
		t.Fatalf("syntax: %d %+v", code, eb)
	}
	if strings.Contains(eb.Error, "unexpected") || strings.Contains(eb.Error, "Go struct") {
		t.Fatalf("decoder internals leaked: %q", eb.Error)
	}
}

// A malformed validate tag fails app.Start rather than every request.
type badTagIn struct {
	N int `json:"n" validate:"minimum=3"`
}

type badTagCtrl struct{}

func (c *badTagCtrl) Routes(r *Router) { Post(r, "/x", c.h) }
func (c *badTagCtrl) h(context.Context, *Req[badTagIn]) (string, error) {
	return "", nil
}

func TestMalformedTagFailsStart(t *testing.T) {
	app := New()
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	var errs []error
	r := &Router{app: app, prefix: "/badtag", errs: &errs}
	(&badTagCtrl{}).Routes(r)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), `unknown validate rule "minimum"`) {
		t.Fatalf("errs = %v", errs)
	}
}
