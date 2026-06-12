package bosun

import (
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

func errStatus(err error) int {
	var e *Error
	if asErr(err, &e) {
		return e.Status
	}
	return http.StatusInternalServerError
}

func publicMessage(err error) string {
	var e *Error
	if asErr(err, &e) {
		return e.Msg // never leak the cause to clients
	}
	return "internal server error"
}

func errOrigin(err error) string {
	var e *Error
	if asErr(err, &e) {
		return e.origin
	}
	return ""
}

func asErr(err error, target **Error) bool {
	for err != nil {
		if e, ok := err.(*Error); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
