package bosun

import (
	"context"
	"fmt"
	"net/http"
)

// --- response status, headers and cookies for typed handlers ---
//
// A typed handler returns only (Out, error), so the rest of the response is
// set through its ctx argument:
//
//	func (c *Notes) Create(ctx context.Context, req *bosun.Req[NewNote]) (Note, error) {
//	    n, err := c.store.Create(ctx, req.Body)
//	    if err != nil {
//	        return Note{}, err
//	    }
//	    bosun.SetStatus(ctx, http.StatusCreated)
//	    bosun.ResponseHeader(ctx).Set("Location", "/notes/"+n.ID)
//	    return n, nil
//	}
//
// The bosun.Status route option sets a fixed success status instead, which
// OpenAPI generation can see.

// responseCtx is the context a typed handler receives: the request context
// plus the response the handler can adjust through SetStatus,
// ResponseHeader and SetCookie. It is reachable from contexts derived from
// it, so helpers called with a child context work too.
type responseCtx struct {
	context.Context
	w      http.ResponseWriter
	status int // success status set by SetStatus; 0 if not set
}

type responseCtxKey struct{}

func (c *responseCtx) Value(key any) any {
	if key == (responseCtxKey{}) {
		return c
	}
	return c.Context.Value(key)
}

func responseOf(ctx context.Context) *responseCtx {
	rc, _ := ctx.Value(responseCtxKey{}).(*responseCtx)
	return rc
}

// SetStatus sets the status a typed handler's successful response is sent
// with, overriding the default 200 and any bosun.Status route option. It
// must be a 2xx or 3xx code; 204, 205 and 304 send no body whatever Out is.
// A returned error still decides the status of an error response. Call it
// with the handler's ctx (or one derived from it); elsewhere it does
// nothing.
func SetStatus(ctx context.Context, code int) {
	if !validSuccessStatus(code) {
		panic(fmt.Sprintf("bosun.SetStatus: %d is not a 2xx or 3xx status", code))
	}
	if rc := responseOf(ctx); rc != nil {
		rc.status = code
	}
}

// ResponseHeader returns the header map of a typed handler's response, to
// set headers such as Location, Cache-Control or Retry-After. Headers apply
// to the response whether the handler succeeds or returns an error, except
// that an error response is always sent as application/json. A
// Content-Type set here replaces the one Out would get on success. Outside
// a typed handler's ctx it returns an empty header that is discarded.
func ResponseHeader(ctx context.Context) http.Header {
	if rc := responseOf(ctx); rc != nil {
		return rc.w.Header()
	}
	return http.Header{}
}

// SetCookie adds a Set-Cookie header to a typed handler's response, as
// http.SetCookie does. Like ResponseHeader it applies to error responses
// too, and does nothing outside a typed handler's ctx.
func SetCookie(ctx context.Context, c *http.Cookie) {
	if rc := responseOf(ctx); rc != nil {
		http.SetCookie(rc.w, c)
	}
}

// successStatus is the Status route option.
type successStatus int

func (successStatus) routeOpt() {}

// Status sets the status a typed route answers with on success, in place of
// 200, and records it on RouteInfo so OpenAPI documents it (with no body
// for 204). It must be a 2xx or 3xx code; mounting fails otherwise.
// SetStatus can still choose another status for one response.
//
//	bosun.Post(r, "/notes", c.Create, bosun.Status(http.StatusCreated))
//	bosun.Delete(r, "/notes/{id}", c.Delete, bosun.Status(http.StatusNoContent))
func Status(code int) RouteOpt { return successStatus(code) }

func validSuccessStatus(code int) bool { return code >= 200 && code <= 399 }

// bodyAllowed reports whether a response with status code may carry a body.
func bodyAllowed(code int) bool {
	return code != http.StatusNoContent && code != http.StatusResetContent && code != http.StatusNotModified
}
