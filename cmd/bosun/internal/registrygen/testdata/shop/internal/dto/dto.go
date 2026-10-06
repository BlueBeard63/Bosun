// Package dto imports bosun but registers nothing.
package dto

import "github.com/bluebeard63/bosun"

type Page = bosun.Req[struct{}]
