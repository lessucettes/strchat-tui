package main

import (
	"log"
	"strchat-tui/client"
	"strchat-tui/tui"
)

func main() {
	// Channel for user actions from the TUI to the client (TUI -> Client)
	actionsChan := make(chan client.UserAction, 10)

	// Channel for display events from the client to the TUI (Client -> TUI)
	eventsChan := make(chan client.DisplayEvent, 10)

	// Create the Nostr client
	nostrClient, err := client.New(actionsChan, eventsChan)
	if err != nil {
		log.Fatalf("Failed to create nostr client: %v", err)
	}

	// Create the TUI
	appUI := tui.New(actionsChan, eventsChan)

	// Run the client in a separate goroutine to manage networking
	go nostrClient.Run()

	// Run the TUI on the main thread (this is a blocking operation)
	if err := appUI.Run(); err != nil {
		log.Fatalf("Failed to run TUI: %v", err)
	}
}
