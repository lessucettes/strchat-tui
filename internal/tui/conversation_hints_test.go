package tui

import (
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestGroupInfoRejectsTyping(t *testing.T) {
	s := newSimTUI(t, 120, 32)
	s.onLoop(func() {
		s.ui.applyEvent(focusGroupState())
		s.ui.render()
		s.ui.openInfo()
		s.ui.input.SetText("draft")
		s.ui.handleKey(tcell.NewEventKey(tcell.KeyRune, 'x', tcell.ModNone))
		if s.ui.lastBell.IsZero() {
			t.Error("typing into group Info did not produce rejection feedback")
		}
		if got := s.ui.input.GetText(); got != "draft" {
			t.Errorf("typing into group Info changed the draft: %q", got)
		}
	})
}
