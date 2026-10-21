package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"github.com/lessucettes/strchat-tui/internal/client"
)

func TestInfoRevealsSelectedMemberAfterMembershipReorder(t *testing.T) {
	s := newSimTUI(t, 58, 10)
	s.onLoop(func() {
		s.ui.applyEvent(focusGroupState())
		s.ui.render()
		s.ui.openInfo()
		s.ui.handleInfoKeys(tcell.NewEventKey(tcell.KeyEnd, 0, tcell.ModNone))
	})
	s.draw()
	s.onLoop(func() {
		state := focusGroupState().Payload.(client.StateUpdate)
		// Push the selected lobby member far down the new list.
		state.Views[0].Children = []string{"coffee", "ucfv0"}
		for i := range 12 {
			name := fmt.Sprintf("topic%d", i)
			state.Views[0].Children = append(state.Views[0].Children, name)
			state.Views = append(state.Views, client.View{Name: name})
		}
		state.Views[0].Children = append(state.Views[0].Children, "lobby")
		s.ui.applyEvent(client.DisplayEvent{Type: "STATE_UPDATE", Payload: state})
		s.ui.render()
	})
	s.draw()
	if !strings.Contains(s.text(), "▶ lobby") {
		t.Fatalf("membership update moved the current destination out of Info:\n%s", s.text())
	}
}
