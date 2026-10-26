package main

import "github.com/bluebeard63/bosun"

// Job is in another binary's main package, so its docs are left out.
type Job struct{}

var _ = bosun.Service[Job]()

func main() {}
