// Package authmod is an installable bosun module. Importing it mounts
// login routes into the host app:
//
//	import _ "github.com/amberstack/bosun/modules/authmod"
//
// Hosts can override Options (register your own *authmod.Options instance),
// replace the UserStore extension point (register your own implementation
// of authmod.UserStore), and remap the prefix with
// bosun.OverridePrefix[authmod.LoginController]("/account").
package authmod

import (
	"fmt"
	"net/http"

	"github.com/amberstack/bosun"
)

// --- extension point: the host provides real user storage ---

type User struct {
	Email string
	Name  string
}

// UserStore is the module's extension point.
type UserStore interface {
	FindByEmail(email string) (User, bool)
}

// memoryStore is the fallback used when the host doesn't bind UserStore.
type memoryStore struct{}

func (memoryStore) FindByEmail(email string) (User, bool) {
	if email == "demo@example.com" {
		return User{Email: email, Name: "Demo User"}, true
	}
	return User{}, false
}

var _ = bosun.Service[memoryStore]()
var _ = bosun.DefaultBind[UserStore, memoryStore]()

// --- overridable configuration ---

type Options struct {
	Greeting string
}

var _ = bosun.Default[*Options](func() *Options {
	return &Options{Greeting: "welcome"}
})

// --- the controller the module ships ---

type LoginController struct {
	store UserStore // injected: host's implementation, or the default
	opts  *Options  // injected: host's options, or the default
}

var _ = bosun.Controller[LoginController]("/auth")

func (c *LoginController) Routes(r *bosun.Router) {
	r.Post("/login", c.Login)
	r.Get("/check/{email}", c.Check)
}

func (c *LoginController) Login(w http.ResponseWriter, r *http.Request) {
	email := r.URL.Query().Get("email")
	u, ok := c.store.FindByEmail(email)
	if !ok {
		http.Error(w, "unknown user", http.StatusUnauthorized)
		return
	}
	fmt.Fprintf(w, "%s, %s\n", c.opts.Greeting, u.Name)
}

func (c *LoginController) Check(w http.ResponseWriter, r *http.Request) {
	_, ok := c.store.FindByEmail(r.PathValue("email"))
	fmt.Fprintf(w, "exists: %v\n", ok)
}
