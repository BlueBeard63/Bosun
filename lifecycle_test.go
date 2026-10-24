package bosun

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bluebeard63/bosun/registry"
)

func TestResolveInstanceAfterShutdown(t *testing.T) {
	app := New()
	if err := app.Shutdown(); err != nil {
		t.Fatal(err)
	}
	got, err := app.Reg.ResolveType(reflect.TypeOf(app))
	if err != nil || got != app {
		t.Fatalf("ResolveType(*App) after Shutdown = %v, %v", got, err)
	}
}

// lcCloser records whether it was closed.
type lcCloser struct{ closed atomic.Bool }

func (c *lcCloser) Close() error { c.closed.Store(true); return nil }

type lcMissingMW struct{}

// failingStartApp returns an app whose Start fails after Validate has built
// a closer, because its app-wide middleware has no provider.
func failingStartApp() (*App, *lcCloser) {
	app := New(WithMiddleware(Use[lcMissingMW]()))
	c := &lcCloser{}
	registry.Register[*lcCloser](app.Reg, func(*registry.Registry) (*lcCloser, error) { return c, nil })
	return app, c
}

func TestStartFailureClosesServices(t *testing.T) {
	t.Run("RunContext", func(t *testing.T) {
		app, c := failingStartApp()
		if err := app.RunContext(context.Background(), "127.0.0.1:0"); err == nil {
			t.Fatal("expected Start error")
		}
		if !c.closed.Load() {
			t.Fatal("built service not closed after Start failure")
		}
	})
	t.Run("Serve", func(t *testing.T) {
		app, c := failingStartApp()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		if err := app.Serve(context.Background(), ln); err == nil {
			t.Fatal("expected Start error")
		}
		if !c.closed.Load() {
			t.Fatal("built service not closed after Start failure")
		}
	})
}

type lcCtxKey struct{}

func TestShutdownCancelsRequestContext(t *testing.T) {
	cases := []struct {
		name string
		opts []Option
		want any // value expected under lcCtxKey in the request context
	}{
		{name: "default"},
		{
			name: "WithHTTPServer BaseContext",
			opts: []Option{WithHTTPServer(func(s *http.Server) {
				s.BaseContext = func(net.Listener) context.Context {
					return context.WithValue(context.Background(), lcCtxKey{}, "kept")
				}
			})},
			want: "kept",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seen := make(chan any, 1) // request context value, sent once ctx is done
			stream := WithMiddleware(UseFunc(func(http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = w.Write([]byte("open\n"))
					w.(http.Flusher).Flush()
					select {
					case <-r.Context().Done():
						seen <- r.Context().Value(lcCtxKey{})
					case <-time.After(10 * time.Second):
					}
				})
			}))
			app := New(append(tc.opts, stream)...)
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- app.Serve(ctx, ln) }()

			resp, err := http.Get("http://" + ln.Addr().String() + "/stream")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			// Once the first line arrives the handler is blocked streaming.
			if line, _ := bufio.NewReader(resp.Body).ReadString('\n'); line != "open\n" {
				t.Fatalf("stream body = %q", line)
			}
			cancel()

			select {
			case v := <-seen:
				if v != tc.want {
					t.Fatalf("context value = %v, want %v", v, tc.want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("handler never saw ctx.Done() after shutdown began")
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("Serve = %v, want clean shutdown", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Serve did not return")
			}
		})
	}
}
