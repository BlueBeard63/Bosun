package users

import (
	"context"

	"example.com/shop/internal/dto"
	"github.com/bluebeard63/bosun"
)

type Repo struct{}

var _ = bosun.Service[Repo]()

// UsersController serves /users.
type UsersController struct {
	Repo *Repo // Injected.
}

var _ = bosun.Controller[UsersController]("/users")

// Routes mounts the user routes. Not a typed handler, so not collected.
func (c *UsersController) Routes(r *bosun.Router) {
	bosun.Get(r, "/", c.List)
	bosun.Get(r, "/{id}", c.Get)
}

// List returns every user's name.
//
// The list is not paged.
func (c *UsersController) List(context.Context, *bosun.Req[struct{}]) ([]string, error) {
	return nil, nil
}

// Get returns one user. Unknown IDs get 404.
func (c UsersController) Get(_ context.Context, req *bosun.Req[dto.UserID]) (dto.User, error) {
	return dto.User{ID: req.Body.ID}, nil
}

// helper is not a handler.
func helper() {}

var _ = helper
