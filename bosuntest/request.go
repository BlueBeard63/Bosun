package bosuntest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/bluebeard63/bosun"
)

// RequestOption configures a request sent by Get, Post, Put, Patch or Delete.
type RequestOption func(*request)

type request struct {
	header  http.Header
	query   url.Values
	body    io.Reader
	session *string
	err     error
}

// JSON sends v, encoded as JSON, as the request body and sets
// Content-Type: application/json.
func JSON(v any) RequestOption {
	return func(r *request) {
		b, err := json.Marshal(v)
		if err != nil {
			r.err = err
			return
		}
		r.body = bytes.NewReader(b)
		r.header.Set("Content-Type", "application/json")
	}
}

// Body sends b as the raw request body; set Content-Type with Header.
func Body(b []byte) RequestOption {
	return func(r *request) { r.body = bytes.NewReader(b) }
}

// Header sets request header k to v.
func Header(k, v string) RequestOption {
	return func(r *request) { r.header.Set(k, v) }
}

// Query adds v to query parameter k. It is appended to any query already in
// the path.
func Query(k, v string) RequestOption {
	return func(r *request) { r.query.Add(k, v) }
}

// Session sends session id the way the app's auth middleware reads it: by
// default as "Authorization: Bearer <id>", or as configured on App with
// SessionHeader or SessionCookie. The app's real middleware still runs and
// looks the id up; swap its session store for a fake with With to make a
// test-only id valid.
func Session(id string) RequestOption {
	return func(r *request) { r.session = &id }
}

// sessionTransport attaches a session id to a request.
type sessionTransport func(req *http.Request, id string)

var defaultSession = bearer("Authorization", "Bearer ")

func bearer(name, prefix string) sessionTransport {
	return func(req *http.Request, id string) { req.Header.Set(name, prefix+id) }
}

// SessionHeader makes Session(id) set header name to prefix+id, e.g.
// SessionHeader("X-Session", "") or the default
// SessionHeader("Authorization", "Bearer ").
func SessionHeader(name, prefix string) Option {
	return func(c *config) { c.session = bearer(name, prefix) }
}

// SessionCookie makes Session(id) send a cookie name=id instead of the
// default bearer header.
func SessionCookie(name string) Option {
	return func(c *config) {
		c.session = func(req *http.Request, id string) {
			req.AddCookie(&http.Cookie{Name: name, Value: id})
		}
	}
}

// Response is the recorded result of a request.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// String returns the body as a string.
func (r *Response) String() string { return string(r.Body) }

// Get sends a GET request for path to app.
func Get(t testing.TB, app *bosun.App, path string, opts ...RequestOption) *Response {
	t.Helper()
	return send(t, app, http.MethodGet, path, opts)
}

// Post sends a POST request for path to app.
func Post(t testing.TB, app *bosun.App, path string, opts ...RequestOption) *Response {
	t.Helper()
	return send(t, app, http.MethodPost, path, opts)
}

// Put sends a PUT request for path to app.
func Put(t testing.TB, app *bosun.App, path string, opts ...RequestOption) *Response {
	t.Helper()
	return send(t, app, http.MethodPut, path, opts)
}

// Patch sends a PATCH request for path to app.
func Patch(t testing.TB, app *bosun.App, path string, opts ...RequestOption) *Response {
	t.Helper()
	return send(t, app, http.MethodPatch, path, opts)
}

// Delete sends a DELETE request for path to app.
func Delete(t testing.TB, app *bosun.App, path string, opts ...RequestOption) *Response {
	t.Helper()
	return send(t, app, http.MethodDelete, path, opts)
}

func send(t testing.TB, app *bosun.App, method, path string, opts []RequestOption) *Response {
	t.Helper()
	rq := &request{header: http.Header{}, query: url.Values{}}
	for _, o := range opts {
		o(rq)
	}
	if rq.err != nil {
		t.Fatalf("bosuntest: %s %s: %v", method, path, rq.err)
	}
	req := httptest.NewRequest(method, path, rq.body)
	for k, vs := range rq.header {
		req.Header[k] = vs
	}
	if len(rq.query) > 0 {
		q := req.URL.Query()
		for k, vs := range rq.query {
			q[k] = append(q[k], vs...)
		}
		req.URL.RawQuery = q.Encode()
		req.RequestURI = req.URL.RequestURI()
	}
	if rq.session != nil {
		configFor(app).session(req, *rq.session)
	}
	return Do(t, app, req)
}

// Do serves req through app.ServeHTTP (app-wide middleware included) and
// records the response.
func Do(t testing.TB, app *bosun.App, req *http.Request) *Response {
	t.Helper()
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("bosuntest: reading response: %v", err)
	}
	return &Response{Status: res.StatusCode, Header: res.Header, Body: body}
}

// Decode decodes the response body as JSON into Out, typically the route's
// Out type. The test fails if the body is not valid JSON for Out. (It is not
// named JSON because that is the request-body option.)
func Decode[Out any](t testing.TB, res *Response) Out {
	t.Helper()
	var out Out
	if err := json.Unmarshal(res.Body, &out); err != nil {
		t.Fatalf("bosuntest: decoding %d response as %s: %v\nbody: %s", res.Status, typeName[Out](), err, res.Body)
	}
	return out
}
