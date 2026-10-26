// Package dto imports bosun but registers nothing.
package dto

import "github.com/bluebeard63/bosun"

// Page is an alias, so it has no schema of its own.
type Page = bosun.Req[struct{}]

// User is a user as the API returns it.
type User struct {
	// ID is the user's key.
	ID   int    `json:"id"`
	Name string `json:"name"` // Display name.
	Bio  string `json:"bio"`
	note string // unexported: never serialised
}

type (
	// UserID selects a user.
	UserID struct {
		ID int `path:"id"` // The user's key.
	}
)

var _ = User{}.note
