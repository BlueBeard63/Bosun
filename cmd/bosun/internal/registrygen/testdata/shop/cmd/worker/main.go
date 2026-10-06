package main

import "github.com/bluebeard63/bosun"

type Job struct{}

var _ = bosun.Service[Job]()

func main() {}
