package tui

import (
	"strings"
	"testing"

	"github.com/lessucettes/strchat-tui/internal/client"
)

// The info pane is the only place that shows whether the client reached a relay,
// so a relay status change has to appear there without any other interaction.
func TestRelayPaneReflectsConnectionChanges(t *testing.T) {
	s := newSimTUI(t, 120, 32)
	s.ui.applyEvent(client.DisplayEvent{Type: "STATE_UPDATE", Payload: client.StateUpdate{Views: []client.View{{Name: "s0000h"}}}})
	s.draw()
	if !strings.Contains(s.text(), "Connected Relays") {
		t.Fatalf("info pane does not list relays:\n%s", s.text())
	}

	s.ui.applyEvent(client.DisplayEvent{Type: "RELAYS_UPDATE", Payload: []client.RelayInfo{
		{URL: "wss://relay.example.com", Connected: true},
		{URL: "wss://silent.example.net", Connected: false},
	}})
	s.draw()
	screen := s.text()
	if !strings.Contains(screen, "relay.example.com") {
		t.Fatalf("info pane lost the relay list:\n%s", screen)
	}
	if strings.Contains(screen, "× relay.example.com") {
		t.Errorf("connected relay rendered as disconnected:\n%s", screen)
	}
	if !strings.Contains(screen, "× silent.example.net") {
		t.Errorf("disconnected relay is not marked as disconnected:\n%s", screen)
	}
}
