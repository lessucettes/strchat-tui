package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"github.com/lessucettes/strchat-tui/internal/client"
)

func TestConversationTitlesKeepPaneNamesAndFocusKeys(t *testing.T) {
	for _, size := range [][2]int{{120, 32}, {80, 32}, {80, 14}, {58, 10}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			s := newSimTUI(t, size[0], size[1])
			s.onLoop(func() {
				state := focusGroupState().Payload.(client.StateUpdate)
				state.Views[0].Name = strings.Repeat("long-group-", 12)
				s.ui.applyEvent(client.DisplayEvent{Type: "STATE_UPDATE", Payload: state})
				s.ui.render()
				s.ui.openInfo()
				s.ui.handleInfoKeys(tcell.NewEventKey(tcell.KeyHome, 0, tcell.ModNone))
				s.ui.render()
				for _, title := range []struct{ got, want string }{
					{s.ui.detailsView.GetTitle(), "Info (Alt+N)"},
					{s.ui.input.GetTitle(), "Input (Alt+I)"},
					{s.ui.outputView.GetTitle(), "Messages (Alt+O)"},
				} {
					if title.got != title.want {
						t.Errorf("pane title contains unexpected context: got %q, want %q", title.got, title.want)
					}
				}
			})
			s.draw()
			for _, want := range []string{"Info (Alt+N)", "Input (Alt+I)"} {
				if screen := s.text(); !strings.Contains(screen, want) {
					t.Errorf("pane name or focus key missing: %q\n%s", want, screen)
				}
			}
			s.onLoop(func() { s.ui.setFocus(s.ui.input) })
			s.draw()
			for _, want := range []string{"Messages (Alt+O)", "Input (Alt+I)"} {
				if screen := s.text(); !strings.Contains(screen, want) {
					t.Errorf("pane name or focus key missing after returning to input: %q\n%s", want, screen)
				}
			}
		})
	}
}
