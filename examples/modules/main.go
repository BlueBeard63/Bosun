package main

import (
	"fmt"
	"log"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/modules/authmod"
	"github.com/bluebeard63/bosun/modules/healthmod"
	"github.com/bluebeard63/bosun/registry"
)

// The host replaces the auth module's UserStore extension point with its own
// storage (in real life: backed by gorm).
type DBUserStore struct{}

func (DBUserStore) FindByEmail(email string) (authmod.User, bool) {
	if email == "jack@amberstack.dev" {
		return authmod.User{Email: email, Name: "Jack"}, true
	}
	return authmod.User{}, false
}

func main() {
	app := bosun.New(
		// remap the health module's routes without touching its code
		bosun.OverridePrefix[healthmod.HealthController]("/status"),
	)

	// fulfil the auth module's extension point with our own implementation
	registry.RegisterInstance[authmod.UserStore](app.Reg, DBUserStore{})

	// override the module's default options
	registry.RegisterInstance[*authmod.Options](app.Reg, &authmod.Options{
		Greeting: "ahoy",
	})

	fmt.Println("listening on :8091")
	log.Fatal(app.Run(":8091"))
}
