// Package users is a test fixture: its Out shares a name, and a last
// import path element, with v1/users.Out.
package users

type Out struct {
	FullName string `json:"full_name"`
	Age      int    `json:"age"`
}
