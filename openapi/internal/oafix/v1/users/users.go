// Package users is a test fixture: its Out shares a name, and a last
// import path element, with v2/users.Out.
package users

type Out struct {
	Name string `json:"name"`
}
