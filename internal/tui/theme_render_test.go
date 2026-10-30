package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"github.com/lessucettes/strchat-tui/internal/client"
)

func TestThemeRecolorsRetainedScrollback(t *testing.T) {
	s := newSimTUI(t, 120, 40)
	s.ui.applyEvent(client.DisplayEvent{Type: "STATE_UPDATE", Payload: client.StateUpdate{
		Views: []client.View{{Name: "lobby"}}, ChatNicks: map[string]string{"lobby": "me"},
	}})
	s.ui.applyEvent(client.DisplayEvent{Type: "INFO", Content: "retained information"})
	s.ui.applyEvent(client.DisplayEvent{Type: "NEW_MESSAGE", Chat: "lobby", Nick: "me", IsOwnMessage: true, Content: "retained message [#32cd32]"})
	s.ui.applyEvent(client.DisplayEvent{Type: "ERROR", Content: "retained error"})
	s.draw()
	for _, name := range []string{"blue-gray", "monochrome", "red-gold", "default"} {
		s.ui.applyEvent(client.DisplayEvent{Type: "THEME_UPDATE", Content: name})
		s.draw()
		s.onLoop(func() {
			want := colorTag(s.ui.theme.titleColor) + "-- retained information"
			if text := s.ui.outputView.GetText(false); !strings.Contains(text, want) {
				t.Errorf("%s: old info was not recolored: want %q", name, want)
			}
			ownColor := s.ui.theme.inputTextColor
			if name == "monochrome" {
				ownColor = tcell.ColorWhite
			}
			if text := s.ui.outputView.GetText(false); !strings.Contains(text, colorBoldTag(ownColor)+"me") {
				t.Errorf("%s: old own message was not recolored", name)
			}
			if text := s.ui.outputView.GetText(true); !strings.Contains(text, "retained message [#32cd32]") {
				t.Errorf("%s: literal color tag was modified: %s", name, text)
			}
			want = colorTag(s.ui.theme.logErrorColor)
			if text := s.ui.logsView.GetText(false); !strings.Contains(text, want) || !strings.Contains(text, "retained error") {
				t.Errorf("%s: old error was not recolored", name)
			}
			if text := s.ui.detailsView.GetText(false); !strings.Contains(text, colorTag(s.ui.theme.logWarnColor)+"Connected Relays:") {
				t.Errorf("%s: cached details were not recolored", name)
			}
		})
		x, y := s.findByPrefix("retained information")
		if x < 0 {
			t.Fatal("retained info is not rendered")
		}
		fg, _, _ := s.styleAt(x, y).Decompose()
		want := onLoopValue(s, func() tcell.Color { return s.ui.theme.titleColor })
		if fg.Hex() != want.Hex() {
			t.Errorf("%s: rendered retained info color = %v, want %v", name, fg, want)
		}
	}
}

// Linux VT exposes eight colors to tcell. Distinct RGB pastels can all become
// the same gray there, so check rendered roles after tcell's palette matching.
func TestThemeReadabilityInLimitedPalettes(t *testing.T) {
	s := newSimTUI(t, 140, 40)
	s.ui.applyEvent(client.DisplayEvent{Type: "STATE_UPDATE", Payload: client.StateUpdate{
		Views: []client.View{{Name: "lobby"}},
	}})
	for i := 0; i < 5; i++ {
		s.ui.applyEvent(client.DisplayEvent{
			Type: "NEW_MESSAGE", Chat: "lobby", Nick: fmt.Sprintf("Voice%d", i),
			FullPubKey: strings.Repeat("00", 31) + fmt.Sprintf("%x0", i+1),
			Content:    "palette body",
		})
	}
	s.ui.applyEvent(client.DisplayEvent{
		Type: "NEW_MESSAGE", Chat: "lobby", Nick: "me", IsOwnMessage: true,
		Content: "palette own message",
	})
	s.ui.applyEvent(client.DisplayEvent{Type: "INFO", Content: "palette notice"})

	s.onLoop(func() { s.ui.input.SetText("palette draft") })
	for _, entry := range themes {
		s.ui.applyEvent(client.DisplayEvent{Type: "THEME_UPDATE", Content: entry.name})
		s.draw()
		styles := make(map[string]tcell.Style)
		for _, text := range []string{"palette body", "palette own message", "palette notice", "palette draft", titleInput, "Voice0", "Voice1", "Voice2", "Voice3", "Voice4"} {
			x, y := s.findByPrefix(text)
			if x < 0 {
				t.Fatalf("%s: %q is not rendered", entry.name, text)
			}
			styles[text] = s.styleAt(x, y)
		}
		border := onLoopValue(s, func() tcell.Color { return s.ui.outputView.GetBorderColor() })
		for _, count := range []int{8, 16, 256} {
			palette := make([]tcell.Color, count)
			for i := range palette {
				palette[i] = tcell.PaletteColor(i)
			}
			foreground := make(map[string]tcell.Color)
			for role, style := range styles {
				fg, bg, _ := style.Decompose()
				fg, bg = tcell.FindColor(fg, palette), tcell.FindColor(bg, palette)
				foreground[role] = fg
				if fg == bg {
					t.Errorf("%s/%d: %s disappears into its background", entry.name, count, role)
				}
			}
			if entry.name == "monochrome" {
				continue
			}
			if foreground[titleInput] == tcell.FindColor(border, palette) {
				t.Errorf("%s/%d: focused and inactive borders merge", entry.name, count)
			}
			if foreground["palette notice"] == foreground["palette body"] {
				t.Errorf("%s/%d: information accent merges with body text", entry.name, count)
			}
			if foreground["palette own message"] == foreground["palette body"] {
				t.Errorf("%s/%d: own messages merge with other messages", entry.name, count)
			}
			nicks := make(map[tcell.Color]bool)
			for i := 0; i < 5; i++ {
				nicks[foreground[fmt.Sprintf("Voice%d", i)]] = true
			}
			if len(nicks) != 5 {
				t.Errorf("%s/%d: five nickname accents collapse to %d colors", entry.name, count, len(nicks))
			}
		}
	}
}

func TestThemeInvalidSelectionAndLocalState(t *testing.T) {
	ui := newIdleTUI(t, 8)
	ui.handleCommand("/theme blue-gray")
	ui.applyEvent(client.DisplayEvent{Type: "THEME_UPDATE", Content: "blue-gray"})
	ui.render()
	selected := ui.theme
	for _, arg := range []string{"0", "5", "-1", "missing", "red-gold extra", "[red]"} {
		ui.handleCommand("/theme " + arg)
		if ui.theme != selected {
			t.Fatalf("invalid argument %q changed the theme", arg)
		}
		if logs := strings.Join(ui.snapshot().logs.lines, "\n"); !strings.Contains(logs, "Unknown theme:") {
			t.Fatalf("invalid argument %q did not produce an error", arg)
		}
	}
	ui.input.SetText("draft")
	ui.rememberInput("previous")
	ui.handleCommand("/theme monochrome")
	ui.applyEvent(client.DisplayEvent{Type: "THEME_UPDATE", Content: "monochrome"})
	ui.render()
	if ui.input.GetText() != "draft" || len(ui.inputHistory) != 1 || ui.inputHistory[0] != "previous" {
		t.Fatal("switching themes reset input or history")
	}
	ui.handleCommand("/t")
	if out := strings.Join(ui.snapshot().output.lines, "\n"); !strings.Contains(out, "monochrome (current)") {
		t.Fatal("theme list does not mark current selection")
	}
}

func TestThemePreservesScrollSelectionAndHiddenPanes(t *testing.T) {
	s := newSimTUI(t, 120, 40)
	s.ui.applyEvent(client.DisplayEvent{Type: "STATE_UPDATE", Payload: client.StateUpdate{
		Views: []client.View{{Name: "lobby"}, {Name: "other"}},
	}})
	for i := 0; i < 80; i++ {
		s.ui.applyEvent(client.DisplayEvent{Type: "INFO", Content: fmt.Sprintf("line %d", i)})
	}
	s.ui.applyEvent(client.DisplayEvent{Type: "ERROR", Content: "hidden error"})
	s.draw()
	s.onLoop(func() {
		s.ui.chatList.SetCurrentItem(1)
		s.ui.selectedForGroup["lobby"] = true
		s.ui.outputView.ScrollTo(10, 0)
		s.ui.applyEvent(client.DisplayEvent{Type: "THEME_UPDATE", Content: "red-gold"})
	})
	s.draw()
	s.onLoop(func() {
		row, _ := s.ui.outputView.GetScrollOffset()
		if row != 10 || s.ui.chatList.GetCurrentItem() != 1 || !s.ui.selectedForGroup["lobby"] {
			t.Errorf("theme switch changed scroll/selection: row %d", row)
		}
	})
	s.resize(58, 10) // Logs and chat list are hidden.
	s.ui.applyEvent(client.DisplayEvent{Type: "THEME_UPDATE", Content: "blue-gray"})
	s.draw()
	s.resize(120, 40)
	s.draw()
	s.onLoop(func() {
		if text := s.ui.logsView.GetText(false); !strings.Contains(text, colorTag(blueGrayTheme.logErrorColor)) {
			t.Error("hidden pane retained old colors after resize")
		}
	})
}

func TestThemeSwitchDuringEventAndLogCollection(t *testing.T) {
	s := newSimTUI(t, 120, 40)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			s.ui.applyEvent(client.DisplayEvent{Type: "INFO", Content: fmt.Sprintf("concurrent info %d", i)})
			fmt.Fprintln(logSink{t: s.ui}, "concurrent logger output")
		}
	}()
	for i := 0; i < 20; i++ {
		s.ui.applyEvent(client.DisplayEvent{Type: "THEME_UPDATE", Content: themes[i%len(themes)].name})
		s.draw()
	}
	<-done
	s.draw()
	if !strings.Contains(onLoopValue(s, func() string { return s.ui.outputView.GetText(true) }), "concurrent info 99") {
		t.Fatal("theme switching lost incoming messages")
	}
}
