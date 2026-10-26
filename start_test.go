package bosun

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// dupRoute makes dupA and dupB both mount GET /dup/x, for one test.
var dupRoute = false

type dupA struct{}
type dupB struct{}

var (
	_ = Controller[dupA]("/dup")
	_ = Controller[dupB]("/dup")
)

func (c *dupA) Routes(r *Router) {
	if dupRoute {
		r.Get("/x", func(http.ResponseWriter, *http.Request) {})
	}
}

func (c *dupB) Routes(r *Router) {
	if dupRoute {
		r.Get("/x", func(http.ResponseWriter, *http.Request) {})
	}
}

func TestDuplicateRouteFailsStart(t *testing.T) {
	dupRoute = true
	defer func() { dupRoute = false }()
	err := New().Start()
	if err == nil {
		t.Fatal("duplicate route did not fail Start")
	}
	for _, want := range []string{"GET /dup/x", "dupA", "dupB"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q doesn't mention %q", err, want)
		}
	}
}

func TestStartTwiceIsIdempotent(t *testing.T) {
	app := New()
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	n := len(app.TypedRoutes())
	if err := app.Start(); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if got := len(app.TypedRoutes()); got != n {
		t.Fatalf("RouteInfo duplicated: %d routes, want %d", got, n)
	}

	// A failed Start keeps returning the same error.
	badRoute = true
	defer func() { badRoute = false }()
	bad := New()
	err1, err2 := bad.Start(), bad.Start()
	if err1 == nil || err1 != err2 {
		t.Fatalf("Start results differ: %v, %v", err1, err2)
	}
}

func TestStartThenServe(t *testing.T) {
	app := New()
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Serve(ctx, ln) }()

	resp, err := http.Get("http://" + ln.Addr().String() + "/t/plain")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(string(body), "plain:") {
		t.Fatalf("got %d %q", resp.StatusCode, body)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
}
