// Package bosun is a zero-ceremony web framework layer over net/http.
//
// Types self-register where they're declared:
//
//	type AuthService struct {
//		users *UserService // injected automatically
//	}
//	var _ = bosun.Service[AuthService]()
//
// MODULES: a module is just a Go package full of these declarations.
// Importing the package installs it — controllers, routes, services and all:
//
//	import _ "github.com/bluebeard63/bosun-modules/auth"
//
// A module's identity is its import path (derived automatically from its
// types). Hosts stay in control:
//
//	app := bosun.New(
//		bosun.OverridePrefix[authmod.LoginController]("/account"), // remap routes
//		bosun.Disable("github.com/bluebeard63/bosun-modules/metrics"),
//	)
//
// Modules ship overridable defaults and declare extension points as
// interfaces the host fulfils:
//
//	// inside the module:
//	var _ = bosun.Default[*Options](func() *Options { return &Options{...} })
//	type UserStore interface { FindByEmail(string) (User, bool) }
//
//	// inside the host:
//	registry.RegisterInstance[authmod.UserStore](app.Reg, &MyStore{})
//
// Injection rule: any field whose type is registered gets injected
// (exported or unexported). Opt out per-field with `inject:"-"`.
// Lifecycle hooks: Init() error after injection; Close() error on Shutdown.
package bosun
