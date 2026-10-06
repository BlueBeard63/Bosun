package testsonly

import "github.com/bluebeard63/bosun"

type fake struct{}

var _ = bosun.Service[fake]()
