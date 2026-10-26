package mw

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bluebeard63/bosun"
)

func etagHandler(opts ETagOptions, h http.HandlerFunc) http.Handler {
	return (&ETag{}).Configure(opts).Handle(h)
}

func textBody(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, body)
	}
}

func condReq(method, inm string) *http.Request {
	r := httptest.NewRequest(method, "/x", nil)
	if inm != "" {
		r.Header.Set("If-None-Match", inm)
	}
	return r
}

func TestETagSetsAndRevalidates(t *testing.T) {
	h := etagHandler(ETagOptions{}, textBody("hello world"))
	rec := serve(h, condReq("GET", ""))
	tag := rec.Header().Get("ETag")
	if rec.Code != 200 || !strings.HasPrefix(tag, `"`) || rec.Body.String() != "hello world" {
		t.Fatalf("status = %d etag = %q body = %q", rec.Code, tag, rec.Body)
	}
	if rec.Header().Get("Content-Length") != "11" {
		t.Fatalf("Content-Length = %q", rec.Header().Get("Content-Length"))
	}
	if again := serve(h, condReq("GET", "")).Header().Get("ETag"); again != tag {
		t.Fatalf("unstable tag: %q vs %q", again, tag)
	}
	if other := serve(etagHandler(ETagOptions{}, textBody("bye")), condReq("GET", "")).Header().Get("ETag"); other == tag {
		t.Fatal("different bodies share a tag")
	}

	for _, inm := range []string{tag, "W/" + tag, `"nope", ` + tag, "*"} {
		rec = serve(h, condReq("GET", inm))
		if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
			t.Fatalf("If-None-Match %q: status = %d body = %q", inm, rec.Code, rec.Body)
		}
		if rec.Header().Get("ETag") != tag || rec.Header().Get("Content-Type") != "" {
			t.Fatalf("304 headers = %v", rec.Header())
		}
	}
	if rec = serve(h, condReq("GET", `"stale"`)); rec.Code != 200 || rec.Body.String() != "hello world" {
		t.Fatalf("stale tag: %d", rec.Code)
	}
}

func TestETagWeak(t *testing.T) {
	rec := serve(etagHandler(ETagOptions{Weak: true}, textBody("x")), condReq("GET", ""))
	if !strings.HasPrefix(rec.Header().Get("ETag"), `W/"`) {
		t.Fatalf("ETag = %q", rec.Header().Get("ETag"))
	}
}

func TestETagSkips(t *testing.T) {
	cases := []struct {
		name   string
		method string
		opts   ETagOptions
		h      http.HandlerFunc
		want   string // expected ETag ("" = none)
	}{
		{"POST", "POST", ETagOptions{}, textBody("x"), ""},
		{"non-200", "GET", ETagOptions{}, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, "missing")
		}, ""},
		{"handler ETag kept", "GET", ETagOptions{}, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("ETag", `"mine"`)
			_, _ = io.WriteString(w, "x")
		}, `"mine"`},
		{"too large", "GET", ETagOptions{MaxBodySize: 4}, textBody("hello world"), ""},
		{"HEAD without body", "HEAD", ETagOptions{}, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "11")
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(etagHandler(tc.opts, tc.h), condReq(tc.method, "*"))
			if got := rec.Header().Get("ETag"); got != tc.want {
				t.Fatalf("ETag = %q, want %q", got, tc.want)
			}
			if tc.want == "" && rec.Code == http.StatusNotModified {
				t.Fatal("304 without a tag")
			}
		})
	}
}

func TestETagLargeBodyStreamsIntact(t *testing.T) {
	body := strings.Repeat("0123456789", 100)
	h := etagHandler(ETagOptions{MaxBodySize: 64}, func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i < 10; i++ {
			_, _ = io.WriteString(w, body[i*100:(i+1)*100])
		}
	})
	rec := serve(h, condReq("GET", ""))
	if rec.Body.String() != body || rec.Header().Get("ETag") != "" {
		t.Fatalf("body len %d, etag %q", rec.Body.Len(), rec.Header().Get("ETag"))
	}
}

func TestETagFlushStopsBuffering(t *testing.T) {
	var flushedBeforeReturn bool
	h := etagHandler(ETagOptions{}, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "data: 1\n\n")
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("Flush: %v", err)
		}
		flushedBeforeReturn = w.(interface{ Unwrap() http.ResponseWriter }).Unwrap().(*httptest.ResponseRecorder).Flushed
		_, _ = io.WriteString(w, "data: 2\n\n")
	})
	rec := serve(h, condReq("GET", ""))
	if !flushedBeforeReturn || rec.Body.String() != "data: 1\n\ndata: 2\n\n" || rec.Header().Get("ETag") != "" {
		t.Fatalf("flushed = %v body = %q etag = %q", flushedBeforeReturn, rec.Body, rec.Header().Get("ETag"))
	}
}

func TestETagInsideCompress(t *testing.T) {
	body := strings.Repeat("compress me ", 200)
	h := (&Compress{}).Configure(CompressOptions{}).Handle(etagHandler(ETagOptions{}, textBody(body)))
	req := condReq("GET", "")
	req.Header.Set("Accept-Encoding", "gzip")
	rec := serve(h, req)
	tag := rec.Header().Get("ETag")
	if rec.Header().Get("Content-Encoding") != "gzip" || !strings.HasPrefix(tag, `W/"`) || rec.Header().Get("Content-Length") != "" {
		t.Fatalf("headers = %v", rec.Header())
	}
	if gunzip(t, rec.Body.Bytes()) != body {
		t.Fatal("round trip mismatch")
	}
	// The browser echoes the weakened tag; it still revalidates.
	req = condReq("GET", tag)
	req.Header.Set("Accept-Encoding", "gzip")
	if rec = serve(h, req); rec.Code != http.StatusNotModified || rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("revalidation: %d %v", rec.Code, rec.Header())
	}
}

func TestETagDynamic(t *testing.T) {
	d := &bosun.Dynamic[ETagOptions]{}
	d.Set(&ETagOptions{})
	h := (&ETag{opts: d}).Handle(textBody("x"))
	if strings.HasPrefix(serve(h, condReq("GET", "")).Header().Get("ETag"), "W/") {
		t.Fatal("weak by default")
	}
	d.Set(&ETagOptions{Weak: true})
	if !strings.HasPrefix(serve(h, condReq("GET", "")).Header().Get("ETag"), "W/") {
		t.Fatal("hot reload not applied")
	}
}

func TestETagListMatches(t *testing.T) {
	tag := `"abc"`
	cases := map[string]bool{
		`"abc"`:          true,
		`W/"abc"`:        true,
		`"x", "abc"`:     true,
		` "x" ,W/"abc" `: true,
		`*`:              true,
		`"abcd"`:         false,
		`abc`:            false,
		`"x"`:            false,
	}
	for in, want := range cases {
		if got := etagListMatches(in, tag); got != want {
			t.Errorf("etagListMatches(%q) = %v, want %v", in, got, want)
		}
	}
}
