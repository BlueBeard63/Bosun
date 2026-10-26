//go:build ignore

// A helper run with `go run aaa_tool.go`. It sorts before users.go, so a
// scanner that ignored build constraints would read this package as main and
// skip its controllers.
package main

func main() {}
