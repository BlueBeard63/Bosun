package bosun

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/amberstack/bosun/registry"
)

// --- typed fixtures ---

type echoIn struct {
	ID      int     `path:"id"`
	Verbose bool    `query:"v"`
	Ratio   float64 `query:"r"`
	Note    string  `json:"note"`
}

type echoOut struct {
	ID    int     `json:"id"`
	V     bool    `json:"v"`
	Ratio float64 `json:"ratio"`
	Note  string  `json:"note"`
	Trace string  `json:"trace,omitempty"`
}

type loginIn struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Key      string `json:"key" audit:"-"`
}

type loginOut struct {
	Token string `json:"token"`
	Name  string `json:"name"`
}

type headerIn struct {
	APIKey string `header:"X-API-Key"`
	Trace  int    `header:"X-Trace"`
}

type headerOut struct {
	APIKey string `json:"api_key"`
	Trace  int    `json:"trace"`
}

type formIn struct {
	Name string `form:"name"`
	Age  int    `form:"age"`
}

type formOut struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

type typedCtrl struct{}

var _ = Controller[typedCtrl]("/api")

func (c *typedCtrl) Routes(r *Router) {
	Get(r, "/echo/{id}", c.echo)
	Post(r, "/login", c.login)
	Post(r, "/boom", c.boom)
	Get(r, "/declared", c.declared, Errors(418))
	Put(r, "/put", c.noop)
	Delete(r, "/del", c.noop)
	Patch(r, "/patch", c.noop)
	Post(r, "/raw", c.raw)
	Get(r, "/none", c.none)
	Get(r, "/header", c.header)
	Post(r, "/form", c.form)
	Post(r, "/any", c.anyBody)
	Get(r, "/colon/:id", c.echo)
	Get(r, "/mixed/:org/items/{id}", c.echo)
	Get(r, "/out/string", c.outString)
	Get(r, "/out/bytes", c.outBytes)
	Get(r, "/out/empty", c.outEmpty)
}

func (c *typedCtrl) echo(ctx context.Context, req *Req[echoIn]) (echoOut, error) {
	in := req.Body
	return echoOut{
		ID:    in.ID,
		V:     in.Verbose,
		Ratio: in.Ratio,
		Note:  in.Note,
		Trace: req.Header.Get("X-Trace-Id"),
	}, nil
}

func (c *typedCtrl) login(ctx context.Context, req *Req[loginIn]) (loginOut, error) {
	if req.Body.Password != "hunter2" {
		return loginOut{}, E(http.StatusUnauthorized, "invalid credentials", errors.New("no user"))
	}
	return loginOut{Token: "tok-123", Name: "Jack"}, nil
}

func (c *typedCtrl) boom(ctx context.Context, _ *Req[struct{}]) (struct{}, error) {
	return struct{}{}, errors.New("internal detail that must not leak")
}

func (c *typedCtrl) declared(ctx context.Context, _ *Req[struct{}]) (struct{ OK bool }, error) {
	return struct{ OK bool }{true}, nil
}

func (c *typedCtrl) noop(ctx context.Context, _ *Req[struct{}]) (struct{ OK bool }, error) {
	return struct{ OK bool }{true}, nil
}

func (c *typedCtrl) raw(ctx context.Context, req *Req[string]) (struct{ Echo string }, error) {
	return struct{ Echo string }{Echo: req.Body}, nil
}

func (c *typedCtrl) none(ctx context.Context, _ *Req[struct{}]) (struct{ OK bool }, error) {
	return struct{ OK bool }{true}, nil
}

func (c *typedCtrl) header(ctx context.Context, req *Req[headerIn]) (headerOut, error) {
	return headerOut{APIKey: req.Body.APIKey, Trace: req.Body.Trace}, nil
}

func (c *typedCtrl) form(ctx context.Context, req *Req[formIn]) (formOut, error) {
	return formOut{Name: req.Body.Name, Age: req.Body.Age}, nil
}

func (c *typedCtrl) anyBody(ctx context.Context, req *Req[any]) (struct{ Got any }, error) {
	return struct{ Got any }{Got: req.Body}, nil
}

func (c *typedCtrl) outString(ctx context.Context, _ *Req[struct{}]) (string, error) {
	return "hello world", nil
}

func (c *typedCtrl) outBytes(ctx context.Context, _ *Req[struct{}]) ([]byte, error) {
	return []byte{0x01, 0x02, 0x03}, nil
}

func (c *typedCtrl) outEmpty(ctx context.Context, _ *Req[struct{}]) (struct{}, error) {
	return struct{}{}, nil
}

// capturing auditor

type capAuditor struct {
	mu     sync.Mutex
	events []AuditEvent
}

func (a *capAuditor) Audit(ctx context.Context, ev AuditEvent) {
	a.mu.Lock()
	a.events = append(a.events, ev)
	a.mu.Unlock()
}

func (a *capAuditor) last(t *testing.T) AuditEvent {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.events) == 0 {
		t.Fatal("no audit events captured")
	}
	return a.events[len(a.events)-1]
}

func newAuditedApp(t *testing.T) (*App, *capAuditor) {
	t.Helper()
	aud := &capAuditor{}
	app := New()
	registry.RegisterInstance[Auditor](app.Reg, aud)
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	return app, aud
}

// --- binding through real routes ---

func TestTypedBinding(t *testing.T) {
	app := newStarted(t)
	rec := do(app, "GET", "/api/echo/42?v=true&r=2.5", "")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"id":42`, `"v":true`, `"ratio":2.5`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in %s", want, body)
		}
	}
}

func TestTypedBindingBodyAndBadJSON(t *testing.T) {
	app := newStarted(t)
	rec := do(app, "GET", "/api/echo/1", `{"note":"hi"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"note":"hi"`) {
		t.Fatalf("body bind failed: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(app, "GET", "/api/echo/1", `{not-json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad JSON should be 400, got %d", rec.Code)
	}
}

func TestRawRequestAccess(t *testing.T) {
	app := newStarted(t)
	req := httptest.NewRequest("GET", "/api/echo/7", nil)
	req.Header.Set("X-Trace-Id", "abc-123")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"trace":"abc-123"`) {
		t.Fatalf("handler should see X-Trace-Id from req.Header: %s", rec.Body.String())
	}
}

func TestRawStringBody(t *testing.T) {
	app := newStarted(t)
	req := httptest.NewRequest("POST", "/api/raw", strings.NewReader("hello world"))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"Echo":"hello world"`) {
		t.Fatalf("string body not echoed: %s", rec.Body.String())
	}
}

func TestEmptyStructSkipsParse(t *testing.T) {
	app := newStarted(t)
	// junk body should be ignored when In is empty struct (no parse attempted)
	req := httptest.NewRequest("GET", "/api/none", strings.NewReader(`{not-json`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("empty-struct route should ignore junk body, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHeaderTagBinding(t *testing.T) {
	app := newStarted(t)
	req := httptest.NewRequest("GET", "/api/header", nil)
	req.Header.Set("X-API-Key", "secret")
	req.Header.Set("X-Trace", "42")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"api_key":"secret"`) || !strings.Contains(body, `"trace":42`) {
		t.Fatalf("header tag binding failed: %s", body)
	}
}

func TestAnyBodyDecodes(t *testing.T) {
	app := newStarted(t)
	rec := do(app, "POST", "/api/any", `{"a":1,"b":"x"}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"a":1`) || !strings.Contains(body, `"b":"x"`) {
		t.Fatalf("any body should JSON-decode: %s", body)
	}
}

func TestAnyBodyEmpty(t *testing.T) {
	app := newStarted(t)
	rec := do(app, "POST", "/api/any", "")
	if rec.Code != 200 {
		t.Fatalf("empty body with In=any should not panic: %d %s", rec.Code, rec.Body.String())
	}
}

func TestReqQueryShortcut(t *testing.T) {
	req := httptest.NewRequest("GET", "/?q=hello&tag=a&tag=b", nil)
	r := &Req[struct{}]{Request: req}
	if r.Query().Get("q") != "hello" {
		t.Fatalf("q = %q", r.Query().Get("q"))
	}
	tags := r.Query()["tag"]
	if len(tags) != 2 || tags[0] != "a" || tags[1] != "b" {
		t.Fatalf("tags = %v", tags)
	}
}

func TestColonPathSyntax(t *testing.T) {
	app := newStarted(t)
	rec := do(app, "GET", "/api/colon/42", "")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"id":42`) {
		t.Fatalf(":id should bind to path:\"id\": %s", rec.Body.String())
	}
}

func TestColonAndBracePathMixed(t *testing.T) {
	app := newStarted(t)
	rec := do(app, "GET", "/api/mixed/acme/items/7", "")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"id":7`) {
		t.Fatalf("{id} segment should still bind: %s", rec.Body.String())
	}
}

func TestRouteInfoNormalizesColons(t *testing.T) {
	found := false
	for _, rt := range TypedRoutes() {
		if rt.Path == "/api/colon/{id}" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("RouteInfo.Path should store the normalized {id} form for OpenAPI")
	}
}

func TestNormalizePath(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"/users/:id", "/users/{id}"},
		{"/users/:id/posts/:slug", "/users/{id}/posts/{slug}"},
		{"/users/{id}", "/users/{id}"},
		{"/health", "/health"},
		{"", ""},
		{"/orgs/:org_id/mix/{a}/:b", "/orgs/{org_id}/mix/{a}/{b}"},
	} {
		if got := normalizePath(c.in); got != c.want {
			t.Fatalf("normalizePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestOutString(t *testing.T) {
	app := newStarted(t)
	rec := do(app, "GET", "/api/out/string", "")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("want text/plain content-type, got %q", ct)
	}
	if rec.Body.String() != "hello world" {
		t.Fatalf("string body should be verbatim, got %q", rec.Body.String())
	}
}

func TestOutBytes(t *testing.T) {
	app := newStarted(t)
	rec := do(app, "GET", "/api/out/bytes", "")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("want octet-stream content-type, got %q", ct)
	}
	got := rec.Body.Bytes()
	if len(got) != 3 || got[0] != 0x01 || got[1] != 0x02 || got[2] != 0x03 {
		t.Fatalf("byte body should be verbatim, got %v", got)
	}
}

func TestOutEmptyStructNoBody(t *testing.T) {
	app := newStarted(t)
	rec := do(app, "GET", "/api/out/empty", "")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("empty struct should write no body, got %q", rec.Body.String())
	}
}

func TestFormTagBinding(t *testing.T) {
	app := newStarted(t)
	req := httptest.NewRequest("POST", "/api/form", strings.NewReader("name=jack&age=30"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"name":"jack"`) || !strings.Contains(body, `"age":30`) {
		t.Fatalf("form tag binding failed: %s", body)
	}
}

func TestBindFieldErrors(t *testing.T) {
	type in struct {
		N int      `query:"n"`
		S []string `query:"s"`
	}
	req := httptest.NewRequest("GET", "/?n=notanumber", nil)
	var v in
	if err := bind(req, &v); err == nil {
		t.Fatal("expected int parse error")
	}
	req = httptest.NewRequest("GET", "/?s=x", nil)
	if err := bind(req, &v); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("expected unsupported kind error, got %v", err)
	}
}

// --- error mapping ---

func TestErrorMappingAndLeakPrevention(t *testing.T) {
	app, aud := newAuditedApp(t)

	rec := do(app, "POST", "/api/login", `{"email":"x","password":"wrong"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "no user") {
		t.Fatal("internal cause leaked to client")
	}
	ev := aud.last(t)
	if !strings.Contains(ev.Err, "no user") {
		t.Fatal("audit should carry the full cause")
	}
	if !strings.Contains(ev.ErrOrigin, "typed_test.go") {
		t.Fatalf("origin should point at the raise site, got %q", ev.ErrOrigin)
	}

	rec = do(app, "POST", "/api/boom", `{}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("plain errors map to 500, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "internal detail") {
		t.Fatal("plain error message leaked to client")
	}
}

func TestErrorHelpers(t *testing.T) {
	cause := errors.New("root")
	e := E(http.StatusTeapot, "public", cause)
	if e.Status != http.StatusTeapot || e.Unwrap() != cause {
		t.Fatal("Error fields wrong")
	}
	if !strings.Contains(e.Error(), "public: root") {
		t.Fatalf("Error() = %q", e.Error())
	}
	wrapped := errAs(e)
	if errStatus(wrapped) != http.StatusTeapot || publicMessage(wrapped) != "public" {
		t.Fatal("unwrap chain broken")
	}
	if errStatus(errors.New("x")) != 500 || publicMessage(errors.New("x")) != "internal server error" {
		t.Fatal("non-bosun errors must default to 500/internal")
	}
	if errOrigin(errors.New("x")) != "" {
		t.Fatal("plain errors have no origin")
	}
}

type wrapErr struct{ inner error }

func (w wrapErr) Error() string { return "wrap: " + w.inner.Error() }
func (w wrapErr) Unwrap() error { return w.inner }

func errAs(e error) error { return wrapErr{inner: e} }

// --- audit redaction ---

func TestAuditRedaction(t *testing.T) {
	app, aud := newAuditedApp(t)
	do(app, "POST", "/api/login", `{"email":"jack@x.dev","password":"hunter2","key":"k1"}`)
	ev := aud.last(t)

	req := ev.Request.(map[string]any)
	if req["password"] != "[REDACTED]" || req["key"] != "[REDACTED]" {
		t.Fatalf("sensitive request fields not redacted: %v", req)
	}
	if req["email"] != "jack@x.dev" {
		t.Fatal("non-sensitive field should pass through")
	}
	resp := ev.Response.(map[string]any)
	if resp["token"] != "[REDACTED]" || resp["name"] != "Jack" {
		t.Fatalf("response redaction wrong: %v", resp)
	}
}

func TestRedactShapes(t *testing.T) {
	type inner struct{ APIKey string }
	type outer struct {
		Items  []inner
		Lookup map[string]string
		Ptr    *inner
		Nil    *inner
	}
	v := outer{
		Items:  []inner{{APIKey: "a"}},
		Lookup: map[string]string{"token": "t", "ok": "v"},
		Ptr:    &inner{APIKey: "b"},
	}
	out := Redact(v).(map[string]any)
	if out["Items"].([]any)[0].(map[string]any)["APIKey"] != "[REDACTED]" {
		t.Fatal("slice element not redacted")
	}
	m := out["Lookup"].(map[string]any)
	if m["token"] != "[REDACTED]" || m["ok"] != "v" {
		t.Fatal("map key redaction wrong")
	}
	if out["Ptr"].(map[string]any)["APIKey"] != "[REDACTED]" {
		t.Fatal("pointer struct not redacted")
	}
	if out["Nil"] != nil {
		t.Fatal("nil pointer should redact to nil")
	}
}

// --- route metadata: declared + observed ---

func TestDeclaredErrorsInRouteIndex(t *testing.T) {
	for _, rt := range TypedRoutes() {
		if rt.Path == "/api/declared" {
			if len(rt.Declared) != 1 || rt.Declared[0] != 418 {
				t.Fatalf("declared errors wrong: %v", rt.Declared)
			}
			return
		}
	}
	t.Fatal("/api/declared not in route index")
}

func TestObservedStatuses(t *testing.T) {
	app := newStarted(t)
	do(app, "POST", "/api/login", `{"password":"hunter2"}`)
	do(app, "POST", "/api/login", `{"password":"nope"}`)
	got := ObservedStatuses("POST", "/api/login")
	has := func(c int) bool {
		for _, g := range got {
			if g == c {
				return true
			}
		}
		return false
	}
	if !has(200) || !has(401) {
		t.Fatalf("observed should include 200 and 401, got %v", got)
	}
}

func TestAllVerbsMount(t *testing.T) {
	app := newStarted(t)
	for _, c := range []struct{ m, p string }{
		{"PUT", "/api/put"}, {"DELETE", "/api/del"}, {"PATCH", "/api/patch"},
	} {
		if rec := do(app, c.m, c.p, `{}`); rec.Code != 200 {
			t.Fatalf("%s %s -> %d", c.m, c.p, rec.Code)
		}
	}
}
