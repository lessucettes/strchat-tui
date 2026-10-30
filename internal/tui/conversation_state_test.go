package tui

import (
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
	ui.applyEvent(client.DisplayEvent{Type: "PRESENCE_UPDATE", Payload: map[string]int{"ucfv0": 3}})
	ui.render()
	ui.openInfo()
	ui.handleInfoKeys(tcell.NewEventKey(tcell.KeyHome, 0, tcell.ModNone))
	if got := ui.detailsView.GetText(true); got != " ▶ coffee\n [3] ucfv0\n [0] lobby" {
		t.Fatalf("active group lost its participant counts: %q", got)
	}
	ui.chatList.SetCurrentItem(4)
	ui.render()
	if got := ui.detailsView.GetText(true); got != " - lobby\n - ucfv0" {
		t.Fatalf("inactive group must show dashes, not participant counts: %q", got)
	}
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
