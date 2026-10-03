package tui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/lessucettes/strchat-tui/internal/client"
)

func TestThemeCommandChangesExistingWidgets(t *testing.T) {
	s := newSimTUI(t, 120, 40)
	originalStyles := tview.Styles
	s.onLoop(func() { s.ui.handleCommand("/theme") })
	s.draw()
	for _, name := range []string{"default", "monochrome", "blue-gray", "red-gold"} {
		if !strings.Contains(s.text(), name) {
			t.Fatalf("/theme did not list %q", name)
		}
	}
	for _, tc := range []struct {
		command, name string
		title, input  tcell.Color
	}{
		{"/theme blue-gray", "blue-gray", tcell.NewHexColor(0x88b9e8), tcell.NewHexColor(0x243447)},
		{"/t red-gold", "red-gold", tcell.NewHexColor(0xe8bd65), tcell.NewHexColor(0x421e24)},
		{"/theme 2", "monochrome", tcell.ColorWhite, tcell.ColorWhite},
		{"/theme default", "default", defaultTheme.titleColor, defaultTheme.inputBgColor},
	} {
		s.onLoop(func() {
			s.ui.input.SetText(tc.command)
			s.ui.input.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), nil)
		})
		select {
		case action := <-s.actions:
			if action.Type != "SET_THEME" || action.Payload != tc.name {
				t.Fatalf("%s submitted %+v", tc.command, action)
			}
		default:
			t.Fatal("theme command did not request persistence")
		}
		// Model the owner's successful-save response, not a local-only switch.
		s.ui.applyEvent(client.DisplayEvent{Type: "THEME_UPDATE", Content: tc.name})
		s.ui.applyEvent(client.DisplayEvent{Type: "INFO", Content: "Theme: " + tc.name})
		s.draw()
		if !strings.Contains(s.text(), "Theme: "+tc.name) {
			t.Fatalf("%s did not confirm the selected theme", tc.command)
		}
		s.onLoop(func() {
			_, bg, _ := s.ui.input.GetFieldStyle().Decompose()
			if bg != tc.input {
				t.Errorf("%s did not recolor existing widgets", tc.command)
			}
			if s.ui.app.GetFocus() != s.ui.input {
				t.Error("theme switch moved focus")
			}
		})
		x, y := s.findByPrefix(titleInput)
		if x < 0 {
			t.Fatal("input title is not rendered")
		}
		fg, _, _ := s.styleAt(x, y).Decompose()
		if fg != tc.title {
			t.Fatalf("rendered title color = %v, want %v", fg, tc.title)
		}
	}
	if tview.Styles != originalStyles {
		t.Fatal("theme switch mutated process-global tview defaults")
	}
	if len(s.actions) != 0 {
		t.Fatal("unexpected extra client actions")
	}
}
