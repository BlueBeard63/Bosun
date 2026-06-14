// Runnable demo for the gormrepomod + repomod modules.
//
//	go run ./examples/repo
//	curl -s -X POST 'localhost:8090/users' -d '{"email":"alice@example.com"}'
//	curl -s 'localhost:8090/users/1'
//	curl -s 'localhost:8090/users?q=@example.com'
package main

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/amberstack/bosun"
	"github.com/amberstack/bosun/modules/gormrepomod"
	"github.com/amberstack/bosun/modules/repomod"
	"github.com/amberstack/bosun/registry"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// --- domain ---

type User struct {
	ID    uint   `gorm:"primaryKey" json:"id"`
	Email string `gorm:"uniqueIndex" json:"email"`
}

var _ = gormrepomod.For[User]()

// --- controller wired through the Repo interface ---

type CreateUserIn struct {
	Email string `json:"email"`
}

type GetUserIn struct {
	ID int `path:"id"`
}

type ListUsersIn struct {
	Q string `query:"q"`
}

type UserOut struct {
	ID    uint   `json:"id"`
	Email string `json:"email"`
}

type UsersController struct {
	Users repomod.Repo[User] // injected via DefaultBind
}

var _ = bosun.Controller[UsersController]("/users")

func (c *UsersController) Routes(r *bosun.Router) {
	bosun.Post(r, "/", c.Create)
	bosun.Get(r, "/{id}", c.Get)
	bosun.Get(r, "/", c.List)
}

func (c *UsersController) Create(ctx context.Context, req *bosun.Req[CreateUserIn]) (UserOut, error) {
	u := &User{Email: req.Body.Email}
	if err := c.Users.Create(ctx, u); err != nil {
		return UserOut{}, bosun.E(http.StatusInternalServerError, "create failed", err)
	}
	return UserOut{ID: u.ID, Email: u.Email}, nil
}

func (c *UsersController) Get(ctx context.Context, req *bosun.Req[GetUserIn]) (UserOut, error) {
	u, err := c.Users.Get(ctx, req.Body.ID)
	if errors.Is(err, repomod.ErrNotFound) {
		return UserOut{}, bosun.E(http.StatusNotFound, "user not found", err)
	}
	if err != nil {
		return UserOut{}, bosun.E(http.StatusInternalServerError, "lookup failed", err)
	}
	return UserOut{ID: u.ID, Email: u.Email}, nil
}

func (c *UsersController) List(ctx context.Context, req *bosun.Req[ListUsersIn]) ([]UserOut, error) {
	q := c.Users.Query().Order("id asc").Limit(50)
	if req.Body.Q != "" {
		q = q.Where("email LIKE ?", "%"+req.Body.Q+"%")
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, bosun.E(http.StatusInternalServerError, "list failed", err)
	}
	out := make([]UserOut, 0, len(rows))
	for _, u := range rows {
		out = append(out, UserOut{ID: u.ID, Email: u.Email})
	}
	return out, nil
}

// --- main ---

func main() {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		log.Fatal(err)
	}
	if err := db.AutoMigrate(&User{}); err != nil {
		log.Fatal(err)
	}

	app := bosun.New()
	registry.RegisterInstance[*gorm.DB](app.Reg, db)
	log.Println("listening on :8090")
	log.Fatal(app.Run(":8090"))
}
