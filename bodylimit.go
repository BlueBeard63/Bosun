package bosun

import (
	"errors"
	"net/http"
	"time"
)

// --- request size limits ---

// DefaultMaxBody is the largest request body a typed route parses unless
// changed with WithMaxBody or the MaxBody route option: 1 MiB.
const DefaultMaxBody int64 = 1 << 20

// DefaultReadHeaderTimeout is the ReadHeaderTimeout of the server Run,
// RunContext and Serve create. WithHTTPServer can change it.
const DefaultReadHeaderTimeout = 10 * time.Second

// WithMaxBody sets the largest request body, in bytes, that typed routes
// parse into In (JSON, form, multipart or a string body). A larger body is
// answered with 413 Request Entity Too Large and the handler is not called.
// Default: DefaultMaxBody (1 MiB). n <= 0 removes the limit. Routes can
// override it with the MaxBody route option; raw handlers (r.Get, ...) are
// never limited.
//
//	app := bosun.New(bosun.WithMaxBody(4 << 20)) // 4 MiB
func WithMaxBody(n int64) Option {
	return func(a *App) { a.maxBody = n }
}

// maxBody is the MaxBody route option.
type maxBody int64

func (maxBody) routeOpt() {}

// MaxBody overrides the app's body limit (WithMaxBody) for one typed route,
// for upload-style endpoints that need more or callbacks that need less.
// n <= 0 removes the limit for the route.
//
//	bosun.Post(r, "/avatar", c.Upload, bosun.MaxBody(20<<20)) // 20 MiB
//
// The app-wide limit applies only to bodies the framework parses, so a route
// whose In is struct{} (it streams req.Request.Body itself) is unlimited
// unless it sets MaxBody, which then caps the raw body too.
func MaxBody(n int64) RouteOpt { return maxBody(n) }

// errBodyTooLarge is the client error for a body over the route's limit.
func errBodyTooLarge(cause error) error {
	return E(http.StatusRequestEntityTooLarge, "request body too large", cause)
}

// bodyTooLarge reports whether err comes from reading past a
// http.MaxBytesReader limit.
func bodyTooLarge(err error) bool {
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe)
}
