package bosun

import (
	"context"
	"errors"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bluebeard63/bosun/registry"
)

// --- lifecycle fixtures ---

// probeLog records lifecycle events in order. Each test registers its own
// instance; the Default keeps other tests' apps valid.
type probeLog struct {
	mu     sync.Mutex
	events []string
}

func (l *probeLog) add(e string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, e)
}

func (l *probeLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.events)
}

var _ = Default[*probeLog](func() *probeLog { return &probeLog{} })

// shutdownProbe is a service whose Close marks when services shut down.
type shutdownProbe struct {
	Log *probeLog
}

func (p *shutdownProbe) Close() error {
	p.Log.add("services closed")
	return nil
}

var _ = Service[shutdownProbe]()

// slowRoute answers /slow by signalling started, then blocking until release
// is closed. It's app-wide middleware so no global controller is needed.
func slowRoute(log *probeLog, started chan<- struct{}, release <-chan struct{}) Option {
	return WithMiddleware(UseFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/slow" {
				next.ServeHTTP(w, r)
				return
			}
			close(started)
			<-release
			log.add("request done")
			w.WriteHeader(http.StatusOK)
		})
	}))
}

type served struct {
	app  *App
	addr string
	done chan error
	log  *probeLog
}

func serveApp(t *testing.T, ctx context.Context, log *probeLog, opts ...Option) *served {
	t.Helper()
	app := New(opts...)
	registry.RegisterInstance[*probeLog](app.Reg, log)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &served{app: app, addr: ln.Addr().String(), done: make(chan error, 1), log: log}
	go func() { s.done <- app.Serve(ctx, ln) }()
	waitReady(t, s.addr)
	return s
}

// waitReady polls until the server answers HTTP.
func waitReady(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get("http://" + addr + "/ready-probe"); err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server at %s never became ready", addr)
}

func waitResult(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for server to return")
		return nil
	}
}

// --- tests ---

func TestServeDrainsInFlightThenClosesServices(t *testing.T) {
	log := &probeLog{}
	started, release := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := serveApp(t, ctx, log, WithShutdownTimeout(5*time.Second), slowRoute(log, started, release))

	type result struct {
		code int
		err  error
	}
	resp := make(chan result, 1)
	go func() {
		r, err := http.Get("http://" + s.addr + "/slow")
		if err != nil {
			resp <- result{err: err}
			return
		}
		r.Body.Close()
		resp <- result{code: r.StatusCode}
	}()
	<-started
	cancel() // begin shutdown with a request in flight

	// New connections are refused once draining starts.
	refused := false
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		c, err := net.Dial("tcp", s.addr)
		if err != nil {
			refused = true
			break
		}
		c.Close()
	}
	if !refused {
		t.Fatal("server still accepting connections after shutdown began")
	}
	if ev := log.snapshot(); len(ev) != 0 {
		t.Fatalf("services closed before in-flight request finished: %v", ev)
	}

	close(release)
	if r := <-resp; r.err != nil || r.code != http.StatusOK {
		t.Fatalf("in-flight request: code=%d err=%v", r.code, r.err)
	}
	if err := waitResult(t, s.done); err != nil {
		t.Fatalf("clean shutdown returned %v, want nil", err)
	}
	if got, want := log.snapshot(), []string{"request done", "services closed"}; !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestServeShutdownTimeoutExceeded(t *testing.T) {
	log := &probeLog{}
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := serveApp(t, ctx, log, WithShutdownTimeout(50*time.Millisecond), slowRoute(log, started, release))

	clientErr := make(chan error, 1)
	go func() {
		r, err := http.Get("http://" + s.addr + "/slow")
		if err == nil {
			r.Body.Close()
		}
		clientErr <- err
	}()
	<-started
	cancel()

	err := waitResult(t, s.done)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "50ms") {
		t.Fatalf("err = %v, want DeadlineExceeded mentioning the timeout", err)
	}
	if !slices.Contains(log.snapshot(), "services closed") {
		t.Fatal("services must still be closed after a timed-out drain")
	}
	if err := <-clientErr; err == nil {
		t.Fatal("stuck request should have its connection closed")
	}
}

func TestServeCleanShutdownReturnsNil(t *testing.T) {
	log := &probeLog{}
	ctx, cancel := context.WithCancel(context.Background())
	s := serveApp(t, ctx, log)
	cancel()
	if err := waitResult(t, s.done); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !slices.Equal(log.snapshot(), []string{"services closed"}) {
		t.Fatalf("events = %v", log.snapshot())
	}
}

func TestServeAppMiddlewareApplies(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := serveApp(t, ctx, &probeLog{}, WithMiddleware(traceFunc("app")))
	r, err := http.Get("http://" + s.addr + "/appmw/typed")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if got := strings.Join(r.Header.Values("X-Trace"), ","); got != "app,controller,route" {
		t.Fatalf("trace = %q", got)
	}
}

func TestRunContextListenFailureClosesServices(t *testing.T) {
	log := &probeLog{}
	app := New()
	registry.RegisterInstance[*probeLog](app.Reg, log)
	err := app.RunContext(context.Background(), "127.0.0.1:-1")
	if err == nil {
		t.Fatal("expected listen error")
	}
	if !slices.Equal(log.snapshot(), []string{"services closed"}) {
		t.Fatalf("events = %v", log.snapshot())
	}
}

func TestServeFailureIsReturned(t *testing.T) {
	log := &probeLog{}
	app := New()
	registry.RegisterInstance[*probeLog](app.Reg, log)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close() // Serve on a dead listener fails immediately

	err = app.Serve(context.Background(), ln)
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("err = %v, want a genuine serve failure", err)
	}
	if !slices.Equal(log.snapshot(), []string{"services closed"}) {
		t.Fatalf("events = %v", log.snapshot())
	}
}

func TestWithHTTPServerConfiguresServer(t *testing.T) {
	var seen *http.Server
	ctx, cancel := context.WithCancel(context.Background())
	s := serveApp(t, ctx, &probeLog{}, WithHTTPServer(func(srv *http.Server) {
		srv.ReadHeaderTimeout = 3 * time.Second
		seen = srv
	}))
	cancel()
	if err := waitResult(t, s.done); err != nil {
		t.Fatal(err)
	}
	if seen == nil || seen.Handler == nil || seen.ReadHeaderTimeout != 3*time.Second {
		t.Fatalf("server not configured: %+v", seen)
	}
}

func TestDefaultShutdownTimeout(t *testing.T) {
	if got := New().shutdownTimeout; got != DefaultShutdownTimeout {
		t.Fatalf("default timeout = %s", got)
	}
}
