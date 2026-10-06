# Running and shutting down

`app.Run` serves HTTP and shuts down gracefully when the process is asked to stop. This guide covers the default behaviour, how to tune it, and how to take full control of the server when you need to.

## The simple pattern

```go
func main() {
    app := bosun.New()
    if err := app.Run(":8080"); err != nil {
        log.Fatal(err)
    }
}
```

`Run` starts the app, listens on the address and serves until the process receives `SIGINT` (Ctrl+C) or `SIGTERM` (what Kubernetes, systemd and Docker send). Then it shuts down in this order:

1. **Stop accepting.** The listener closes, so new connections are refused and idle keep-alive connections are closed.
2. **Drain.** Requests already in flight get up to the shutdown timeout (10 seconds by default) to finish.
3. **Force close.** When the timeout expires, any remaining connections are closed.
4. **Close services.** Every registered `io.Closer` is closed in reverse dependency order, for example an `AuthService` before the `*gorm.DB` it uses. This happens only after draining, so in-flight requests never see a closed database or client.

A clean shutdown returns `nil`. `Run` returns an error when the listener can't be opened (for example, the port is already in use), when the server fails, when draining exceeds the timeout (the error wraps `context.DeadlineExceeded`), or when a service's `Close` fails. Several errors are joined into one. `http.ErrServerClosed` is the expected result of a shutdown and is never returned.

After the first signal, `Run` stops intercepting signals, so pressing Ctrl+C a second time kills the process straight away.

Check the error rather than wrapping the call in `log.Fatal(app.Run(...))`. `log.Fatal(nil)` would log `<nil>` and exit with status 1 after a clean shutdown.

## Tuning the server

```go
app := bosun.New(
    bosun.WithShutdownTimeout(30*time.Second), // grace period for in-flight requests
    bosun.WithHTTPServer(func(s *http.Server) {
        s.ReadHeaderTimeout = 5 * time.Second // protects against slow-header clients
        s.IdleTimeout = 2 * time.Minute
    }),
)
```

`WithHTTPServer` receives the `*http.Server` that `Run`, `RunContext` and `Serve` use, with `Handler` already set to `app.Handler()`. Use it for timeouts, `TLSConfig`, `ErrorLog` or `MaxHeaderBytes`.

In Kubernetes, set the shutdown timeout below `terminationGracePeriodSeconds` (default 30s) so draining completes before the pod is killed.

## Controlling when to stop

`RunContext` shuts down when a context is cancelled instead of on OS signals. Use it to combine signals with your own stop conditions, or to run the app alongside other components under `errgroup`.

```go
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
defer stop()

if err := app.RunContext(ctx, ":8080"); err != nil {
    log.Fatal(err)
}
```

`Serve` is the same as `RunContext`, but takes a `net.Listener` you have already opened. This is useful for socket activation, unix sockets, TLS listeners, or tests on a random port.

```go
ln, _ := net.Listen("tcp", "127.0.0.1:0")
go app.Serve(ctx, ln) // Serve takes ownership of ln
```

## Managing your own server

When you need full control (several listeners, HTTP/3, a custom shutdown sequence), call `Start` and serve `app.Handler()` yourself. You then own the shutdown order. Call `app.Shutdown()` after your server has drained.

```go
if err := app.Start(); err != nil {
    log.Fatal(err)
}
srv := &http.Server{Addr: ":8080", Handler: app.Handler()}

go func() {
    if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
        log.Fatal(err)
    }
}()

ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer stop()
<-ctx.Done()

shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()
if err := srv.Shutdown(shutdownCtx); err != nil { // 1-3: stop accepting, drain
    log.Print(err)
}
if err := app.Shutdown(); err != nil { // 4: close services
    log.Print(err)
}
```

Serve `app.Handler()`, not `app.Mux`, so that [app-wide middleware](./middleware.md#app-wide-middleware) still applies.

## Long-lived connections

`http.Server.Shutdown` does not wait for hijacked connections such as WebSockets. A server-sent events handler that blocks on its own loop also holds draining open until the timeout. Make such handlers return when the request context is done or when your own stop signal fires, so they finish within the grace period.
