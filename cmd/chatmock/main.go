package main

import (
	"log"

	"chatmock/internal/app"
)

func main() {
	server := app.NewServer()
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
