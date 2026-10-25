package tui

import (
	"testing"

	"github.com/gdamore/tcell/v2"

	"github.com/lessucettes/strchat-tui/internal/client"
)

func TestSavedGroupSelectionRestoresAndQueuesChanges(t *testing.T) {
	actions := make(chan client.UserAction, 16)
	ui := New(actions, make(chan client.DisplayEvent))
	t.Cleanup(ui.restoreLog)
	state := focusGroupState().Payload.(client.StateUpdate)
	state.Views[0].SelectedChat = "lobby"
	ui.applyEvent(client.DisplayEvent{Type: "STATE_UPDATE", Payload: state})
	if got := ui.conversationChat(ui.snapshot()); got != "lobby" {
		t.Fatalf("saved selection was not available before rendering: %q", got)
	}
	ui.render()
	ui.openInfo()
	ui.handleInfoKeys(tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone))
	select {
	case action := <-actions:
		if action.Type != "SELECT_GROUP_CHAT" || action.View != "mixed" || action.Chat != "ucfv0" {
			t.Fatalf("selection did not reach the config owner: %+v", action)
		}
	default:
		t.Fatal("selection was only stored in the TUI")
	}
	// A queued snapshot must not roll back a newer local keyboard selection.
	ui.applyEvent(client.DisplayEvent{Type: "STATE_UPDATE", Payload: state})
	ui.render()
	if got := ui.conversationChat(ui.snapshot()); got != "ucfv0" {
		t.Fatalf("stale snapshot rolled back the keyboard choice: %q", got)
	}
}

func TestGroupSelectionQueueFailureKeepsCurrentChat(t *testing.T) {
	ui := newIdleTUI(t, 0)
	state := focusGroupState().Payload.(client.StateUpdate)
	state.Views[0].SelectedChat = "lobby"
	ui.applyEvent(client.DisplayEvent{Type: "STATE_UPDATE", Payload: state})
	ui.render()
	ui.openInfo()
	ui.handleInfoKeys(tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone))
	if got := ui.conversationChat(ui.snapshot()); got != "lobby" {
		t.Fatalf("rejected selection changed the destination: %q", got)
	}
	if !ui.snapshot().dropNotice {
		t.Fatal("selection backpressure was not surfaced")
	}
}
