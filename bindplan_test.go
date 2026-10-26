package bosun

import (
	"context"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

type bindBenchIn struct {
	Org    string `path:"org"`
	ID     int    `path:"id"`
	Limit  int    `query:"limit"`
	Offset int    `query:"offset"`
	Q      string `query:"q"`
	Sort   string `query:"sort"`
	Trace  string `header:"X-Trace"`
}

type bindPlanCtrl struct{}

var _ = Controller[bindPlanCtrl]("/bindplan")

func (c *bindPlanCtrl) Routes(r *Router) {
	Get(r, "/{org}/items/{id}", func(_ context.Context, req *Req[bindBenchIn]) (struct{}, error) {
		return struct{}{}, nil
	})
}

func TestBindSlicesAndPointers(t *testing.T) {
	type in struct {
		Tags  []string `query:"tag"`
		IDs   []int    `query:"id"`
		Limit *int     `query:"limit"`
		Off   *int     `query:"off"`
		Langs []string `header:"Accept-Language"`
		Picks []string `form:"pick"`
	}
	req := httptest.NewRequest("POST", "/?tag=a&tag=&tag=b&id=1&id=2&limit=0",
		strings.NewReader("pick=x&pick=y"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Add("accept-language", "en")
	req.Header.Add("Accept-Language", "fr")
	var v in
	if err := bind(req, &v); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(v.Tags, []string{"a", "b"}) || !slices.Equal(v.IDs, []int{1, 2}) {
		t.Fatalf("repeated query: %+v", v)
	}
	if v.Limit == nil || *v.Limit != 0 || v.Off != nil {
		t.Fatalf("pointers: limit=%v off=%v", v.Limit, v.Off)
	}
	if !slices.Equal(v.Langs, []string{"en", "fr"}) || !slices.Equal(v.Picks, []string{"x", "y"}) {
		t.Fatalf("header/form slices: %+v", v)
	}

	// An element that doesn't parse is a 400 naming the field, as for scalars.
	req = httptest.NewRequest("GET", "/?id=1&id=x", nil)
	fields := fieldsOf(t, bind(req, &in{}))
	if len(fields) != 1 || fields[0].Field != "id" || fields[0].In != "query" || fields[0].Message != "must be an integer" {
		t.Fatalf("fields = %+v", fields)
	}
	req = httptest.NewRequest("GET", "/?limit=99999999999999999999", nil)
	fields = fieldsOf(t, bind(req, &in{}))
	if len(fields) != 1 || fields[0].Rule != "range" || fields[0].Message != "is out of range for int" {
		t.Fatalf("fields = %+v", fields)
	}
}

func TestUnsupportedBindTagFailsMount(t *testing.T) {
	type mapIn struct {
		M map[string]string `query:"m"`
	}
	type timeIn struct {
		At time.Time `header:"X-At"`
	}
	type pathSliceIn struct {
		IDs []int `path:"ids"`
	}
	type unexportedIn struct {
		id int `path:"id"` //nolint:unused // the binder must reject unexported fields
	}
	app := newStarted(t)
	var errs []error
	r := &Router{app: app, prefix: "/bindbad", errs: &errs}
	Get(r, "/map", func(context.Context, *Req[mapIn]) (struct{}, error) { return struct{}{}, nil })
	Get(r, "/time", func(context.Context, *Req[timeIn]) (struct{}, error) { return struct{}{}, nil })
	Get(r, "/{ids}", func(context.Context, *Req[pathSliceIn]) (struct{}, error) { return struct{}{}, nil })
	Get(r, "/u/{id}", func(context.Context, *Req[unexportedIn]) (struct{}, error) {
		return struct{}{}, nil
	})
	want := []string{
		"GET /map: invalid binding tag: field M: query tag on unsupported type map[string]string",
		"GET /time: invalid binding tag: field At: header tag on unsupported type time.Time",
		"GET /{ids}: invalid binding tag: field IDs: path tag on unsupported type []int",
		"GET /u/{id}: invalid binding tag: field id: path tag on an unexported field",
	}
	if len(errs) != len(want) {
		t.Fatalf("errs = %v", errs)
	}
	for i, err := range errs {
		if err.Error() != want[i] {
			t.Fatalf("errs[%d] = %q, want %q", i, err, want[i])
		}
	}
}

// BenchmarkBindPathQuery binds two path, four query and one header field.
func BenchmarkBindPathQuery(b *testing.B) {
	app := startWithAuditor(b, nil)
	req := httptest.NewRequest("GET", "/bindplan/acme/items/42?limit=50&offset=100&q=boat&sort=name", nil)
	req.Header.Set("X-Trace", "t-1")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		app.Mux.ServeHTTP(httptest.NewRecorder(), req)
	}
}
