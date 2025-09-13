// main.go
package main

import (
	"log"
	"strchat-tui/client"
	"strchat-tui/tui"
)

func main() {
	actionsChan := make(chan client.UserAction, 10)
	eventsChan := make(chan client.DisplayEvent, 10)

	// The client now loads its own configuration.
	nostrClient, err := client.New(actionsChan, eventsChan)
	if err != nil {
		log.Fatalf("Failed to create nostr client: %v", err)
	}

	appUI := tui.New(actionsChan, eventsChan)

	go nostrClient.Run()

	if err := appUI.Run(); err != nil {
		log.Fatalf("Failed to run TUI: %v", err)
	}
}
