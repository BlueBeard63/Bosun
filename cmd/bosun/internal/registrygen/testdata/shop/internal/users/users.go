package users

import (
	"context"

	"github.com/bluebeard63/bosun"
)

type Repo struct{}

var _ = bosun.Service[Repo]()

type UsersController struct {
	Repo *Repo
}

var _ = bosun.Controller[UsersController]("/users")

func (c *UsersController) Routes(r *bosun.Router) { bosun.Get(r, "/", c.List) }

func (c *UsersController) List(context.Context, *bosun.Req[struct{}]) ([]string, error) {
	return nil, nil
}
