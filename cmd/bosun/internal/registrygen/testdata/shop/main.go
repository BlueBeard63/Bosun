package main

//go:generate bosun gen registry

import (
	"log"

	"github.com/bluebeard63/bosun"
)

func main() {
	if err := bosun.New().Run(":8080"); err != nil {
		log.Fatal(err)
	}
}
