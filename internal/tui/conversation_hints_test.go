package tui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestGroupHintsExposeInfoSelectionAndReturn(t *testing.T) {
	s := newSimTUI(t, 58, 10)
	s.onLoop(func() { s.ui.applyEvent(focusGroupState()); s.ui.render() })
	if text := onLoopValue(s, func() string { return s.ui.hints.GetText(true) }); !strings.Contains(text, "Alt+N") {
		t.Fatalf("group composer does not advertise destination selection: %q", text)
	}
	s.onLoop(func() { s.ui.openInfo(); s.ui.render() })
	if text := onLoopValue(s, func() string { return s.ui.hints.GetText(true) }); !strings.Contains(text, "Alt+I") || !strings.Contains(text, "Chat") {
		t.Fatalf("group Info does not advertise member selection and return: %q", text)
	}
}

func TestGroupInfoRejectsTyping(t *testing.T) {
	s := newSimTUI(t, 120, 32)
	s.onLoop(func() {
		s.ui.applyEvent(focusGroupState())
		s.ui.render()
		s.ui.openInfo()
		s.ui.handleKey(tcell.NewEventKey(tcell.KeyRune, 'x', tcell.ModNone))
		if s.ui.lastBell.IsZero() {
			t.Error("typing into group Info did not produce rejection feedback")
		}
	})
}
