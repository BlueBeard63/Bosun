package nested

import "github.com/bluebeard63/bosun"

type N struct{}

var _ = bosun.Service[N]()
