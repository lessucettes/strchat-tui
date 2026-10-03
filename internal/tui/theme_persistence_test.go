package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lessucettes/strchat-tui/internal/client"
)

func TestSavedThemeEventRestoresWidgets(t *testing.T) {
	s := newSimTUI(t, 120, 40)
	s.ui.applyEvent(client.DisplayEvent{Type: "THEME_UPDATE", Content: "red-gold"})
	s.draw()
	if got := onLoopValue(s, func() *theme { return s.ui.theme }); got != redGoldTheme {
		t.Fatal("saved theme was not restored from client event")
	}
	if len(s.actions) != 0 {
		t.Fatal("restoring a saved theme requested another config save")
	}
}

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
	if ui.theme != defaultTheme || !strings.Contains(strings.Join(ui.snapshot().logs.lines, "\n"), "queue full") {
		t.Fatal("queue backpressure was hidden or changed the theme")
	}
}

func TestThemePersistsThroughClientRestart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("HOME", home)
	t.Setenv("APPDATA", home)
	configDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"select", "restart"} {
		t.Run(phase, func(t *testing.T) {
			s := newSimTUI(t, 120, 40)
			c, err := client.New(s.actions, s.events)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() { defer close(done); c.Run() }()
			t.Cleanup(func() {
				c.Stop()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("client did not stop")
				}
			})
			if phase == "select" {
				s.onLoop(func() { s.ui.handleCommand("/t 4") })
			}
			waitFor(t, 5*time.Second, func() bool {
				return s.matches(func() bool { return s.ui.theme == redGoldTheme })
			})
			data, err := os.ReadFile(filepath.Join(configDir, "strchat-tui", "config.json"))
			if err != nil {
				t.Fatal(err)
			}
			var cfg struct {
				Theme string `json:"theme"`
			}
			if err := json.Unmarshal(data, &cfg); err != nil || cfg.Theme != "red-gold" {
				t.Fatalf("saved theme = %q, decode error: %v", cfg.Theme, err)
			}
		})
	}
}
