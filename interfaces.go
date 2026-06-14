package bosun

import "net/http"

// MiddlewareHandler is implemented by middleware types.
type MiddlewareHandler interface {
	Handle(next http.Handler) http.Handler
}

// BaseController is implemented by controllers. The framework discovers
// controller types by this interface; bosun.Controller[T] panics at startup
// if T does not satisfy it.
type BaseController interface {
	Routes(r *Router)
}

type initer interface{ Init() error }
