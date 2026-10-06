package billing

import (
	"net/http"

	b "github.com/bluebeard63/bosun"
)

type Store interface{ Get(id string) string }

type memStore struct{}

func (memStore) Get(string) string { return "" }

var (
	_ = b.Service[memStore]()
	_ = b.DefaultBind[Store, memStore]()
)

type Auth struct{}

func (Auth) Handle(next http.Handler) http.Handler { return next }

var _ = b.Middleware[Auth]()
