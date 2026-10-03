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
		Views: []client.View{{Name: "lobby"}}, Nick: "me",
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
			if text := s.ui.outputView.GetText(false); !strings.Contains(text, colorBoldTag(s.ui.theme.inputTextColor)+"me") {
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

func TestThemeNicknamePaletteMapping(t *testing.T) {
	for _, entry := range themes {
		t.Run(entry.name, func(t *testing.T) {
			r := entry.style.scrollbackColors()
			for i, tag := range defaultTheme.nickPalette {
				got := tcell.GetColor(strings.Trim(r.Replace(tag), "[]")).Hex()
				want := tcell.GetColor(strings.Trim(entry.style.nickPalette[i%len(entry.style.nickPalette)], "[]")).Hex()
				if got != want {
					t.Errorf("palette entry %d maps to %x, want %x", i, got, want)
				}
			}
		})
	}
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
