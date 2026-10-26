package mw

import (
	"bufio"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/bluebeard63/bosun"
)

var bigJSON = `{"data":"` + strings.Repeat("abcdefgh", 512) + `"}`

func compressHandler(opts CompressOptions, h http.HandlerFunc) http.Handler {
	return (&Compress{}).Configure(opts).Handle(h)
}

func gzipReq(ae string) *http.Request {
	r := httptest.NewRequest("GET", "/x", nil)
	if ae != "" {
		r.Header.Set("Accept-Encoding", ae)
	}
	return r
}

func gunzip(t *testing.T, b []byte) string {
	t.Helper()
	zr, err := gzip.NewReader(strings.NewReader(string(b)))
	if err != nil {
		t.Fatalf("not gzip: %v", err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	return string(out)
}

func writeJSON(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(body))) // must be dropped when compressing
		w.Header().Set("ETag", `"v1"`)
		_, _ = io.WriteString(w, body)
	}
}

func TestCompressGzip(t *testing.T) {
	h := compressHandler(CompressOptions{}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Accept-Ranges", "bytes")
		// several small writes cross MinSize mid-stream
		for i := 0; i < 8; i++ {
			_, _ = io.WriteString(w, bigJSON[i*len(bigJSON)/8:(i+1)*len(bigJSON)/8])
		}
	})
	rec := serve(h, gzipReq("gzip, deflate, br"))
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q", rec.Header().Get("Content-Encoding"))
	}
	if rec.Header().Get("Vary") != "Accept-Encoding" {
		t.Fatalf("Vary = %q", rec.Header().Get("Vary"))
	}
	if rec.Header().Get("Content-Length") != "" || rec.Header().Get("Accept-Ranges") != "" {
		t.Fatalf("length/ranges headers kept: %v", rec.Header())
	}
	if rec.Header().Get("ETag") != `W/"v1"` {
		t.Fatalf("ETag = %q, want weak", rec.Header().Get("ETag"))
	}
	if got := gunzip(t, rec.Body.Bytes()); got != bigJSON {
		t.Fatal("round trip mismatch")
	}
	if rec.Body.Len() >= len(bigJSON) {
		t.Fatal("body not smaller")
	}
}

func TestCompressDropsDeclaredContentLength(t *testing.T) {
	rec := serve(compressHandler(CompressOptions{}, writeJSON(bigJSON)), gzipReq("gzip"))
	if rec.Header().Get("Content-Encoding") != "gzip" || rec.Header().Get("Content-Length") != "" {
		t.Fatalf("headers = %v", rec.Header())
	}
	if gunzip(t, rec.Body.Bytes()) != bigJSON {
		t.Fatal("round trip mismatch")
	}
}

func TestCompressDeflatePreferred(t *testing.T) {
	rec := serve(compressHandler(CompressOptions{}, writeJSON(bigJSON)), gzipReq("gzip;q=0.5, deflate"))
	if rec.Header().Get("Content-Encoding") != "deflate" {
		t.Fatalf("Content-Encoding = %q", rec.Header().Get("Content-Encoding"))
	}
	zr, err := zlib.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(zr)
	if string(out) != bigJSON {
		t.Fatal("deflate round trip mismatch")
	}
}

func TestCompressSkips(t *testing.T) {
	cases := []struct {
		name string
		ae   string
		h    http.HandlerFunc
	}{
		{"no Accept-Encoding", "", writeJSON(bigJSON)},
		{"gzip refused", "gzip;q=0, deflate;q=0", writeJSON(bigJSON)},
		{"brotli only", "br", writeJSON(bigJSON)},
		{"tiny body", "gzip", writeJSON(`{"ok":true}`)},
		{"non-compressible type", "gzip", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			_, _ = io.WriteString(w, bigJSON)
		}},
		{"already encoded", "gzip", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Encoding", "br")
			_, _ = io.WriteString(w, bigJSON)
		}},
		{"no-transform", "gzip", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-transform")
			_, _ = io.WriteString(w, bigJSON)
		}},
		{"partial content", "gzip", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = io.WriteString(w, bigJSON)
		}},
		{"not modified", "gzip", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotModified)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(compressHandler(CompressOptions{}, tc.h), gzipReq(tc.ae))
			if ce := rec.Header().Get("Content-Encoding"); ce == "gzip" || ce == "deflate" {
				t.Fatalf("compressed: %q", ce)
			}
			if !strings.Contains(strings.Join(rec.Header().Values("Vary"), ","), "Accept-Encoding") {
				t.Fatalf("missing Vary: %v", rec.Header())
			}
			if strings.HasPrefix(rec.Body.String(), "\x1f\x8b") {
				t.Fatal("body is gzip")
			}
		})
	}
}

func TestCompressSniffsContentType(t *testing.T) {
	html := "<!DOCTYPE html><html><body>" + strings.Repeat("<p>hello</p>", 200) + "</body></html>"
	rec := serve(compressHandler(CompressOptions{}, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, html)
	}), gzipReq("gzip"))
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("Content-Type = %q, want sniffed from the plain body", rec.Header().Get("Content-Type"))
	}
	if gunzip(t, rec.Body.Bytes()) != html {
		t.Fatal("round trip mismatch")
	}
}

func TestCompressKeepsHandlerVary(t *testing.T) {
	rec := serve(compressHandler(CompressOptions{}, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Vary", "Origin") // Set, not Add: clobbers anything set earlier
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, bigJSON)
	}), gzipReq("gzip"))
	if v := strings.Join(rec.Header().Values("Vary"), ","); v != "Origin,Accept-Encoding" {
		t.Fatalf("Vary = %q", v)
	}
}

func TestCompressMinSizeAndTypesOptions(t *testing.T) {
	h := compressHandler(CompressOptions{MinSize: 1, ContentTypes: []string{"application/x-custom"}},
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/x-custom; charset=utf-8")
			_, _ = io.WriteString(w, "hi")
		})
	rec := serve(h, gzipReq("gzip"))
	if rec.Header().Get("Content-Encoding") != "gzip" || gunzip(t, rec.Body.Bytes()) != "hi" {
		t.Fatalf("custom type not compressed: %v", rec.Header())
	}
}

func TestCompressConfigurePanicsOnBadLevel(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	(&Compress{}).Configure(CompressOptions{Level: 42})
}

func TestCompressDynamic(t *testing.T) {
	d := &bosun.Dynamic[CompressOptions]{}
	d.Set(&CompressOptions{})
	h := (&Compress{opts: d}).Handle(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "short")
	}))
	if rec := serve(h, gzipReq("gzip")); rec.Header().Get("Content-Encoding") != "" {
		t.Fatal("short body compressed under defaults")
	}
	d.Set(&CompressOptions{MinSize: 1})
	if rec := serve(h, gzipReq("gzip")); rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("hot-reloaded MinSize not applied")
	}
}

func TestNegotiateEncoding(t *testing.T) {
	cases := map[string]string{
		"":                       "",
		"gzip":                   "gzip",
		"deflate":                "deflate",
		"*":                      "gzip",
		"*;q=0":                  "",
		"gzip;q=0, *":            "deflate",
		"deflate, gzip":          "gzip",
		"deflate;q=1, gzip;q=.8": "deflate",
		"GZIP;Q=0.1":             "gzip",
		"identity":               "",
		"br, zstd":               "",
	}
	for in, want := range cases {
		var vals []string
		if in != "" {
			vals = []string{in}
		}
		if got := negotiateEncoding(vals); got != want {
			t.Errorf("negotiateEncoding(%q) = %q, want %q", in, got, want)
		}
	}
}

// Recover inside Compress: the 500 must be plain JSON with no gzip header.
func TestCompressWithRecoverInside(t *testing.T) {
	rcv, _ := newRecover(t)
	h := compressHandler(CompressOptions{}, func(w http.ResponseWriter, r *http.Request) {
		rcv.Handle(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			panic("boom")
		})).ServeHTTP(w, r)
	})
	rec := serve(h, gzipReq("gzip"))
	assertPlain500(t, rec)
}

// Recover outside Compress: the buffered response is discarded and the 500
// is written straight to the client, uncompressed.
func TestCompressWithRecoverOutside(t *testing.T) {
	rcv, _ := newRecover(t)
	h := rcv.Handle(compressHandler(CompressOptions{}, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "partial") // still buffered (< MinSize)
		panic("boom")
	}))
	assertPlain500(t, serve(h, gzipReq("gzip")))
}

func assertPlain500(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	if ce := rec.Header().Get("Content-Encoding"); ce != "" {
		t.Fatalf("Content-Encoding %q on the 500", ce)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["error"] != "internal server error" {
		t.Fatalf("body = %q (%v)", rec.Body, err)
	}
}

// Flush must push compressed bytes to the client before the handler returns.
func TestCompressStreamsOnFlush(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(compressHandler(CompressOptions{}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: one\n\n")
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("Flush: %v", err)
		}
		<-release
		_, _ = io.WriteString(w, "data: two\n\n")
	}))
	defer srv.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Accept-Encoding", "gzip")
	tr := &http.Transport{DisableCompression: true}
	defer tr.CloseIdleConnections()
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q", resp.Header.Get("Content-Encoding"))
	}
	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(zr)
	first := make([]byte, len("data: one\n\n"))
	if _, err := io.ReadFull(br, first); err != nil || string(first) != "data: one\n\n" {
		t.Fatalf("first event = %q, %v", first, err) // would hang if Flush didn't reach the client
	}
	close(release)
	rest, _ := io.ReadAll(br)
	if string(rest) != "data: two\n\n" {
		t.Fatalf("rest = %q", rest)
	}
}

func TestCompressForwardsHijackAndUnwrap(t *testing.T) {
	srv := httptest.NewServer(compressHandler(CompressOptions{}, func(w http.ResponseWriter, r *http.Request) {
		if w.(interface{ Unwrap() http.ResponseWriter }).Unwrap() == nil {
			t.Error("Unwrap returned nil")
		}
		conn, bufrw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("Hijack: %v", err)
			return
		}
		defer conn.Close()
		_, _ = bufrw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 6\r\nConnection: close\r\n\r\nraw ok")
		_ = bufrw.Flush()
	}))
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "raw ok" {
		t.Fatalf("body = %q", b)
	}
}
