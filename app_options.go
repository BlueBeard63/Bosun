package bosun

import "reflect"

// --- host options ---

// Option configures an App.
type Option func(*App)

// Disable skips every service, controller, and default declared in the
// package with the given import path (i.e. uninstalls a module at runtime).
func Disable(pkgPath string) Option {
	return func(a *App) { a.disabled[pkgPath] = true }
}

// OverridePrefix remounts controller T's routes under a different prefix.
func OverridePrefix[T any](prefix string) Option {
	t := reflect.TypeOf((*T)(nil))
	return func(a *App) { a.prefixes[t] = prefix }
}

// WithMiddleware registers app-wide middleware. It wraps the whole mux, so
// it runs for every request (including framework endpoints, unmatched
// routes/404s and OPTIONS preflights), outside any controller, group or
// route middleware:
//
//	app -> controller -> group -> route -> typed adapter -> handler
//
// Within the list the first entry is outermost. Accepts the same refs as
// routes: bosun.Use[T](), bosun.Use[T](args...) and bosun.UseFunc(f).
// Repeated calls append. Resolution errors surface from app.Start().
//
//	app := bosun.New(bosun.WithMiddleware(
//	    bosun.Use[mw.Correlation](),
//	    bosun.Use[mw.Logging](),
//	))
func WithMiddleware(mws ...MWRef) Option {
	return func(a *App) { a.mws = append(a.mws, mws...) }
}
