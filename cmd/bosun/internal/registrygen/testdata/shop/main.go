package main

//go:generate bosun gen registry

import (
	"log"

	"github.com/bluebeard63/bosun"
)

// Banner is printed at start; package main docs are keyed "main.".
type Banner struct {
	Text string
}

func main() {
	if err := bosun.New().Run(":8080"); err != nil {
		log.Fatal(err)
	}
}
