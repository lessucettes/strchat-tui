package tui

import (
	"strings"
	"testing"

	"github.com/lessucettes/strchat-tui/internal/client"
)

func TestThemeWaitsForSuccessfulSave(t *testing.T) {
	ui := newIdleTUI(t, 1)
	ui.handleCommand("/theme red-gold")
	ui.render()
	if ui.theme != defaultTheme {
		t.Fatal("theme changed before save acknowledgement")
	}
	ui.applyEvent(client.DisplayEvent{Type: "ERROR", Content: "Failed to save theme"})
	ui.render()
	if ui.theme != defaultTheme || !strings.Contains(strings.Join(ui.snapshot().logs.lines, "\n"), "Failed to save theme") {
		t.Fatal("failed save was hidden or changed the theme")
	}
	ui.handleCommand("/theme blue-gray") // Queue remains full.
	ui.render()
	if ui.theme != defaultTheme || !strings.Contains(strings.Join(ui.snapshot().logs.lines, "\n"), "queue is full") {
		t.Fatal("queue backpressure was hidden or changed the theme")
	}
}
