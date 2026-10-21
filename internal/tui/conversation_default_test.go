package tui

import (
	"testing"

	"github.com/gdamore/tcell/v2"

	"github.com/lessucettes/strchat-tui/internal/client"
)

func TestGroupDefaultAndRememberedSelections(t *testing.T) {
	ui := newIdleTUI(t, 16)
	state := focusGroupState().Payload.(client.StateUpdate)
	state.Views = append(state.Views, client.View{Name: "other", IsGroup: true, Children: []string{"ucfv0", "coffee"}})
	apply := func() {
		ui.applyEvent(client.DisplayEvent{Type: "STATE_UPDATE", Payload: state})
		ui.render()
	}
	ui.applyEvent(client.DisplayEvent{Type: "STATE_UPDATE", Payload: state})
	if chat := ui.conversationChat(ui.snapshot()); chat != "coffee" {
		t.Fatalf("initial destination before the first render = %q, want coffee", chat)
	}
	ui.render()
	ui.openInfo()
	ui.handleInfoKeys(tcell.NewEventKey(tcell.KeyEnd, 0, tcell.ModNone))
	if chat := ui.conversationChat(ui.snapshot()); chat != "lobby" {
		t.Fatalf("explicit selection = %q, want lobby", chat)
	}
	state.ActiveViewIndex = 4
	apply()
	if chat := ui.conversationChat(ui.snapshot()); chat != "ucfv0" {
		t.Fatalf("second group's initial destination = %q, want ucfv0", chat)
	}
	ui.chatList.SetCurrentItem(4)
	ui.openInfo()
	ui.handleInfoKeys(tcell.NewEventKey(tcell.KeyEnd, 0, tcell.ModNone))
	state.ActiveViewIndex = 0
	state.Views[0].Children = []string{"ucfv0", "lobby", "coffee"}
	apply()
	if chat := ui.conversationChat(ui.snapshot()); chat != "lobby" {
		t.Fatalf("returning to a reordered group lost its selection: %q", chat)
	}
	state.Views = state.Views[1:]
	state.ActiveViewIndex = 3
	apply()
	if chat := ui.conversationChat(ui.snapshot()); chat != "coffee" {
		t.Fatalf("other group's remembered destination = %q, want coffee", chat)
	}
	if _, exists := ui.groupFocus["mixed"]; exists {
		t.Fatal("deleted group's selection was not pruned")
	}
}
