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
