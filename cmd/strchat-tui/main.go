package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/lessucettes/strchat-tui/internal/client"
	"github.com/lessucettes/strchat-tui/internal/tui"
)

// Stamped at build time by the Mage targets.
var (
	version = "dev"
	commit  = ""
	date    = ""
)

// versionLine is what --version prints: the version, plus build provenance when
// the build stamped it.
func versionLine() string {
	parts := []string{version}
	if commit != "" {
		parts = append(parts, "commit "+commit)
	}
	if date != "" {
		parts = append(parts, "built "+date)
	}
	return strings.Join(parts, " ")
}

func main() {
	versionFlag := flag.Bool("version", false, "Print the version and exit")
	vFlag := flag.Bool("v", false, "Print the version and exit (shorthand)")
	flag.Parse()

	if *versionFlag || *vFlag {
		fmt.Println(versionLine())
		os.Exit(0)
	}

	actionsChan := make(chan client.UserAction, 32)
	eventsChan := make(chan client.DisplayEvent, 256)

	nostrClient, err := client.New(actionsChan, eventsChan)
	if err != nil {
		log.Fatalf("Failed to create nostr client: %v", err)
	}

	appUI := tui.New(actionsChan, eventsChan)

	done := make(chan struct{})
	go func() {
		nostrClient.Run()
		close(eventsChan)
		close(done)
	}()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		select {
		case <-signals:
			nostrClient.Stop()
		case <-done:
		}
	}()
	err = appUI.Run()
	nostrClient.Stop()
	<-done
	if err != nil {
		log.Fatalf("Failed to run TUI: %v", err)
	}
}
