package bosun

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBindMultipartFormFields(t *testing.T) {
	type in struct {
		Name string `form:"name"`
		Age  int    `form:"age"`
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("name", "jack")
	_ = mw.WriteField("age", "30")
	fw, _ := mw.CreateFormFile("avatar", "a.png")
	_, _ = fw.Write([]byte("png"))
	_ = mw.Close()

	req := httptest.NewRequest("POST", "/", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	var v in
	if err := bind(req, &v); err != nil {
		t.Fatal(err)
	}
	if v.Name != "jack" || v.Age != 30 {
		t.Fatalf("multipart fields not bound: %+v", v)
	}
	// Files stay reachable through the request for raw access.
	if _, fh, err := req.FormFile("avatar"); err != nil || fh.Filename != "a.png" {
		t.Fatalf("FormFile: %v %v", fh, err)
	}
}

func TestBindInvalidMultipart(t *testing.T) {
	type in struct {
		Name string `form:"name"`
	}
	req := httptest.NewRequest("POST", "/", strings.NewReader("not multipart"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=xyz")
	var v in
	err := bind(req, &v)
	ve, ok := err.(*ValidationError)
	if !ok || ve.Message != "invalid form body" {
		t.Fatalf("err = %v, want invalid form body", err)
	}
}

func TestBindNumberOverflow(t *testing.T) {
	type in struct {
		I8  int8    `query:"i8"`
		U8  uint8   `query:"u8"`
		I16 int16   `query:"i16"`
		I32 int32   `query:"i32"`
		F32 float32 `query:"f32"`
	}
	for _, q := range []string{"i8=300", "i8=-129", "u8=256", "i16=40000", "i32=3000000000", "f32=1e40"} {
		req := httptest.NewRequest("GET", "/?"+q, nil)
		var v in
		fields := fieldsOf(t, bind(req, &v))
		if len(fields) != 1 || fields[0].Rule != "range" || fields[0].In != "query" {
			t.Fatalf("%s: fields = %+v", q, fields)
		}
	}
	req := httptest.NewRequest("GET", "/?i8=-128&u8=255&i16=32767&i32=-2147483648&f32=3.4e38", nil)
	var v in
	if err := bind(req, &v); err != nil {
		t.Fatalf("in-range values: %v", err)
	}
	if v.I8 != -128 || v.U8 != 255 || v.I16 != 32767 || v.I32 != -2147483648 {
		t.Fatalf("got %+v", v)
	}
}

type bindIn struct {
	N    int8   `query:"n" validate:"max=100"`
	Name string `json:"name"`
}

type bindCtrl struct{}

func (c *bindCtrl) h(_ context.Context, req *Req[bindIn]) (bindIn, error) {
	return req.Body, nil
}

func (c *bindCtrl) ptr(_ context.Context, req *Req[*bindIn]) (string, error) {
	return "", nil
}

func bindApp(t *testing.T) *App {
	t.Helper()
	app := newStarted(t)
	var errs []error
	r := &Router{app: app, prefix: "/bind", errs: &errs}
	Post(r, "/x", (&bindCtrl{}).h)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	return app
}

func TestBindOverflowIs400(t *testing.T) {
	code, eb := send(t, bindApp(t), "POST", "/bind/x?n=300", "", "", nil)
	if code != 400 || len(eb.Fields) != 1 || eb.Fields[0].Field != "n" || eb.Fields[0].Rule != "range" {
		t.Fatalf("got %d %+v", code, eb)
	}
}

func TestBindContentTypes(t *testing.T) {
	app := bindApp(t)
	for _, ct := range []string{"", "application/json", "Application/JSON; charset=utf-8", "application/merge-patch+json"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/bind/x", strings.NewReader(`{"name":"ann"}`))
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		app.ServeHTTP(rec, req)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"name":"ann"`) {
			t.Fatalf("%q: %d %s", ct, rec.Code, rec.Body.String())
		}
	}
	for _, ct := range []string{"text/plain", "not a media type"} {
		code, eb := send(t, app, "POST", "/bind/x", ct, `{"name":"ann"}`, nil)
		if code != http.StatusUnsupportedMediaType || eb.Error != "unsupported media type" {
			t.Fatalf("%q: got %d %+v", ct, code, eb)
		}
	}
	// No body: the content type does not matter.
	if code, _ := send(t, app, "POST", "/bind/x", "text/plain", "", nil); code != 200 {
		t.Fatalf("empty text/plain body: %d", code)
	}
}

func TestPointerInFailsMount(t *testing.T) {
	app := newStarted(t)
	var errs []error
	r := &Router{app: app, prefix: "/bindptr", errs: &errs}
	Post(r, "/x", (&bindCtrl{}).ptr)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "must not be a pointer; use Req[bosun.bindIn]") {
		t.Fatalf("errs = %v", errs)
	}
}
