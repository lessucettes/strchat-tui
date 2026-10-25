package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"github.com/lessucettes/strchat-tui/internal/client"
)

func TestConversationSelectionSurvivesStateUpdatesByName(t *testing.T) {
	ui := newIdleTUI(t, 16)
	ui.applyEvent(focusGroupState())
	ui.render()
	ui.openInfo()
	ui.handleInfoKeys(tcell.NewEventKey(tcell.KeyHome, 0, tcell.ModNone))
	state := focusGroupState().Payload.(client.StateUpdate)
	state.Views[0], state.Views[2] = state.Views[2], state.Views[0]
	state.ActiveViewIndex = 2
	ui.applyEvent(client.DisplayEvent{Type: "STATE_UPDATE", Payload: state})
	ui.applyEvent(client.DisplayEvent{Type: "RELAYS_UPDATE", Payload: []client.RelayInfo{{URL: "ws://127.0.0.1:1"}}})
	ui.render()
	if got := ui.conversationChat(ui.snapshot()); got != "coffee" {
		t.Fatalf("state/list reorder changed destination: %q", got)
	}
	state.Views[2].Children = []string{"ucfv0", "lobby"}
	ui.applyEvent(client.DisplayEvent{Type: "STATE_UPDATE", Payload: state})
	ui.render()
	if got := ui.conversationChat(ui.snapshot()); got != "ucfv0" {
		t.Fatalf("removing the selected member did not select the first remaining chat: %q", got)
	}
	if ui.groupFocus["mixed"] != "ucfv0" {
		t.Fatal("replacement destination was not remembered")
	}
}

func TestInactiveGroupInfoIsPreviewOnly(t *testing.T) {
	ui := newIdleTUI(t, 16)
	state := focusGroupState().Payload.(client.StateUpdate)
	state.Views = append(state.Views, client.View{Name: "preview", IsGroup: true, Children: []string{"lobby", "ucfv0"}})
	ui.applyEvent(client.DisplayEvent{Type: "STATE_UPDATE", Payload: state})
	ui.render()
	ui.openInfo()
	ui.handleInfoKeys(tcell.NewEventKey(tcell.KeyHome, 0, tcell.ModNone))
	ui.chatList.SetCurrentItem(4)
	ui.render()
	if result := ui.handleInfoKeys(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone)); result == nil {
		t.Fatal("inactive group preview consumed an arrow as a routing command")
	}
	if got := ui.conversationChat(ui.snapshot()); got != "coffee" {
		t.Fatalf("preview changed the active group's destination: %q", got)
	}
	if ui.input.GetTitle() != titleInput {
		t.Fatal("preview changed composer title")
	}
}

func TestConversationThreadKeepsScrolledViewportAndThemes(t *testing.T) {
	s := newSimTUI(t, 80, 32)
	s.onLoop(func() {
		s.ui.applyEvent(focusGroupState())
		for i := range 80 {
			ev := testMessage(i)
			ev.Chat = []string{"coffee", "ucfv0"}[i%2]
			s.ui.applyEvent(ev)
		}
		s.ui.render()
		s.ui.outputView.ScrollTo(10, 0)
		s.ui.openInfo()
		s.ui.handleInfoKeys(tcell.NewEventKey(tcell.KeyHome, 0, tcell.ModNone))
	})
	s.draw()
	for _, entry := range themes {
		s.onLoop(func() {
			s.ui.applyEvent(client.DisplayEvent{Type: "THEME_UPDATE", Content: entry.name})
			s.ui.render()
			s.ui.handleInfoKeys(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
			s.ui.handleInfoKeys(tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone))
		})
		s.draw()
		if row := onLoopValue(s, func() int { row, _ := s.ui.outputView.GetScrollOffset(); return row }); row != 10 {
			t.Fatalf("theme %s or conversation switch moved viewport to row %d", entry.name, row)
		}
		if !strings.Contains(s.text(), "●") {
			t.Fatalf("theme %s lost thread nodes", entry.name)
		}
	}
}

func TestGroupInfoLongListScrollsSelectionIntoView(t *testing.T) {
	for _, size := range [][2]int{{58, 10}, {80, 14}, {80, 32}, {120, 32}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			s := newSimTUI(t, size[0], size[1])
			s.onLoop(func() {
				state := focusGroupState().Payload.(client.StateUpdate)
				for i := range 20 {
					name := fmt.Sprintf("topic%d", i)
					state.Views[0].Children = append(state.Views[0].Children, name)
					state.Views = append(state.Views, client.View{Name: name})
				}
				s.ui.applyEvent(client.DisplayEvent{Type: "STATE_UPDATE", Payload: state})
				s.ui.render()
				s.ui.openInfo()
				s.ui.handleInfoKeys(tcell.NewEventKey(tcell.KeyEnd, 0, tcell.ModNone))
			})
			s.draw()
			if !strings.Contains(s.text(), "▶ topic19") {
				t.Fatalf("last member is not visible in Info:\n%s", s.text())
			}
			s.onLoop(func() {
				s.ui.handleInfoKeys(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
				if got := s.ui.conversationChat(s.ui.snapshot()); got != "topic19" {
					t.Fatalf("arrow past list end wrapped unexpectedly: %q", got)
				}
			})
		})
	}
}
