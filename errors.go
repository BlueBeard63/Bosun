package bosun

import (
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
)

// --- errors with origin ---

// Error carries an HTTP status, a public message, an optional cause, and the
// file:line + function where it was created.
type Error struct {
	Status int
	Msg    string
	Cause  error
	origin string
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Msg, e.Cause)
	}
	return e.Msg
}
func (e *Error) Unwrap() error { return e.Cause }

// E creates an *Error capturing where it was raised.
//
//	return out, bosun.E(http.StatusUnauthorized, "invalid credentials", err)
func E(status int, msg string, cause error) *Error {
	origin := "unknown"
	if pc, file, line, ok := runtime.Caller(1); ok {
		fn := "?"
		if f := runtime.FuncForPC(pc); f != nil {
			fn = f.Name()
		}
		short := file
		if idx := strings.LastIndex(file, "/"); idx >= 0 {
			short = file[idx+1:]
		}
		origin = fmt.Sprintf("%s:%d (%s)", short, line, fn)
	}
	return &Error{Status: status, Msg: msg, Cause: cause, origin: origin}
}

// --- mapping errors to responses ---

// errorMapping is one MapError registration.
type errorMapping struct {
	target error
	status int
	msg    string
}

// MapError makes typed handlers answer with status and msg when they return
// an error matching target (errors.Is, so wrapped errors match too), instead
// of a 500. Use it for sentinels returned from deep inside services, so
// handlers don't each translate them:
//
//	app := bosun.New(
//	    bosun.MapError(billing.ErrCardDeclined, http.StatusPaymentRequired, "card declined"),
//	    bosun.MapError(repomod.ErrConflict, http.StatusConflict, ""),
//	)
//
// An empty msg uses the status text ("conflict"). A *bosun.Error in the
// returned chain still wins; mappings are tried in registration order; and
// they override an error's own HTTPStatus (see errors.md). The mapping is
// per App. MapError panics if target is nil or status is not a valid HTTP
// status code.
func MapError(target error, status int, msg string) Option {
	if target == nil || !validStatus(status) {
		panic(fmt.Sprintf("bosun.MapError(%v, %d): need a non-nil target and a status in 100-999", target, status))
	}
	if msg == "" {
		msg = statusMessage(status)
	}
	return func(a *App) { a.errMaps = append(a.errMaps, errorMapping{target, status, msg}) }
}

// httpStatuser is implemented by errors that carry their own HTTP status,
// such as repomod.ErrNotFound and storemod.ErrNotFound (404). Packages
// implement it without importing bosun, so modules can give their sentinels
// a default status without an import cycle.
type httpStatuser interface{ HTTPStatus() int }

// publicMessager optionally supplies the client-facing message for an
// httpStatuser; without it the status text is used. Error() is never shown.
type publicMessager interface{ PublicMessage() string }

func validStatus(s int) bool { return s >= 100 && s <= 999 }

// statusMessage is the lower-case status text, matching the framework's own
// messages ("internal server error").
func statusMessage(status int) string {
	if t := http.StatusText(status); t != "" {
		return strings.ToLower(t)
	}
	return "error"
}

// knownError resolves the status and public message for err, in order: a
// *Error in the chain (its Status, or 500 if invalid, and its Msg), the
// first matching MapError mapping, then an error carrying its own
// HTTPStatus. ok is false when none applies.
func knownError(maps []errorMapping, err error) (status int, msg string, ok bool) {
	var e *Error
	if asErr(err, &e) {
		status = e.Status
		if !validStatus(status) {
			status = http.StatusInternalServerError
		}
		return status, e.Msg, true // never leak the cause to clients
	}
	for _, m := range maps {
		if errors.Is(err, m.target) {
			return m.status, m.msg, true
		}
	}
	var hs httpStatuser
	if errors.As(err, &hs) {
		if status = hs.HTTPStatus(); validStatus(status) {
			if pm, ok := hs.(publicMessager); ok {
				return status, pm.PublicMessage(), true
			}
			return status, statusMessage(status), true
		}
	}
	return 0, "", false
}

// errorResponse is the status and public message a typed handler's error
// is answered with on this app: see knownError, falling back to 500.
func (a *App) errorResponse(err error) (int, string) {
	return errorResponse(a.errMaps, err)
}

func errorResponse(maps []errorMapping, err error) (int, string) {
	if status, msg, ok := knownError(maps, err); ok {
		return status, msg
	}
	return http.StatusInternalServerError, "internal server error"
}

// errStatus is the HTTP status for err without app mappings: an *Error's
// Status, an error's own HTTPStatus, or 500.
func errStatus(err error) int {
	s, _ := errorResponse(nil, err)
	return s
}

// publicMessage is the client-facing message for err without app mappings.
func publicMessage(err error) string {
	_, m := errorResponse(nil, err)
	return m
}

func errOrigin(err error) string {
	var e *Error
	if asErr(err, &e) {
		return e.origin
	}
	return ""
}

// asErr finds the first *Error in err's tree, following both Unwrap() error
// and Unwrap() []error (errors.Join, fmt.Errorf with several %w).
func asErr(err error, target **Error) bool {
	return errors.As(err, target)
}
