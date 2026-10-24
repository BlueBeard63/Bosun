package bosun

import (
	"bytes"
	"mime/multipart"
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
