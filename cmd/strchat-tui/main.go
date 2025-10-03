package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/lessucettes/strchat-tui/internal/client"
	"github.com/lessucettes/strchat-tui/internal/tui"
)

var version = "dev"

func main() {
	versionFlag := flag.Bool("version", false, "Print the version and exit")
	vFlag := flag.Bool("v", false, "Print the version and exit (shorthand)")
	flag.Parse()

	if *versionFlag || *vFlag {
		fmt.Println(version)
		os.Exit(0)
	}

	actionsChan := make(chan client.UserAction, 10)
	eventsChan := make(chan client.DisplayEvent, 10)

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
