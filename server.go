package bosun

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// --- server lifecycle ---

// DefaultShutdownTimeout is the grace period in-flight requests get once
// shutdown begins, unless overridden with WithShutdownTimeout.
const DefaultShutdownTimeout = 10 * time.Second

// Run starts the app and serves HTTP on addr until the process receives
// SIGINT or SIGTERM, then shuts down gracefully (see RunContext). A clean
// shutdown returns nil:
//
//	if err := app.Run(":8080"); err != nil {
//	    log.Fatal(err)
//	}
//
// After the first signal, a second SIGINT/SIGTERM is no longer intercepted
// and terminates the process immediately.
func (a *App) Run(addr string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	context.AfterFunc(ctx, stop) // restore default handling: a second signal kills
	return a.RunContext(ctx, addr)
}

// RunContext starts the app, listens on addr and serves until ctx is done,
// then shuts down gracefully:
//
//  1. the server stops accepting new connections, idle ones are closed and
//     request contexts are cancelled;
//  2. in-flight requests get up to the shutdown timeout (WithShutdownTimeout)
//     to finish, after which remaining connections are closed;
//  3. services are closed (io.Closer) in reverse dependency order.
//
// It returns nil after a clean shutdown. A listen/serve failure, a shutdown
// that exceeded its timeout, and service Close errors are returned (joined).
// RunContext does not handle OS signals itself; use Run for that.
func (a *App) RunContext(ctx context.Context, addr string) error {
	if err := a.Start(); err != nil {
		return errors.Join(err, a.Shutdown()) // release services Start already built
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return errors.Join(err, a.Shutdown())
	}
	return a.serve(ctx, ln)
}

// Serve is RunContext on an existing listener — for socket activation,
// custom listeners (TLS, unix sockets) or tests on 127.0.0.1:0. It starts
// the app, serves on ln until ctx is done, then shuts down as RunContext
// does. Serve takes ownership of ln and closes it.
func (a *App) Serve(ctx context.Context, ln net.Listener) error {
	if err := a.Start(); err != nil {
		_ = ln.Close()
		return errors.Join(err, a.Shutdown())
	}
	return a.serve(ctx, ln)
}

// serve runs the started app on ln and owns the graceful-shutdown sequence.
func (a *App) serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           a.Handler(),
		ReadHeaderTimeout: DefaultReadHeaderTimeout, // bound slow-header clients
	}
	for _, configure := range a.serverConfig {
		configure(srv)
	}
	// Request contexts derive from base, which is cancelled as shutdown
	// begins so streaming and long-poll handlers can return early. A
	// BaseContext set via WithHTTPServer is kept and wrapped.
	base, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	userBase := srv.BaseContext
	srv.BaseContext = func(l net.Listener) context.Context {
		if userBase == nil {
			return base
		}
		ctx, cancel := context.WithCancel(userBase(l))
		context.AfterFunc(base, cancel)
		return ctx
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case err := <-serveErr:
		// The server failed on its own (not via Shutdown): still release services.
		return errors.Join(fmt.Errorf("bosun: serve: %w", err), a.Shutdown())
	case <-ctx.Done():
	}

	cancelBase()
	sctx, cancel := context.WithTimeout(context.Background(), a.shutdownTimeout)
	defer cancel()
	var errs []error
	if err := srv.Shutdown(sctx); err != nil {
		errs = append(errs, fmt.Errorf("bosun: graceful shutdown did not finish within %s: %w", a.shutdownTimeout, err))
		_ = srv.Close() // drop connections still in flight
	}
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		errs = append(errs, fmt.Errorf("bosun: serve: %w", err))
	}
	// Services close only once the server has drained, so in-flight
	// requests never see a closed database or client.
	if err := a.Shutdown(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
