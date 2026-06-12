package bosun

import "net/http"

// MiddlewareHandler is implemented by middleware types.
type MiddlewareHandler interface {
	Handle(next http.Handler) http.Handler
}

// HasRoutes is implemented by controllers.
type HasRoutes interface {
	Routes(r *Router)
}

type initer interface{ Init() error }
