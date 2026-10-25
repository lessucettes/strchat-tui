package tui

import (
	"testing"

	"github.com/lessucettes/strchat-tui/internal/client"
)

func TestUnchangedMessageRecipientDoesNotRequeryDraft(t *testing.T) {
	actions := make(chan client.UserAction, 64)
	ui := New(actions, nil)
	t.Cleanup(ui.restoreLog)
	ui.applyEvent(focusGroupState())
	ui.input.SetText("@peer#beef hi")
	ui.render()
	for len(actions) > 0 {
		<-actions
	}
	message := testMessage(1)
	message.Chat = "coffee"
	message.RecipientUnchanged = true
	ui.applyEvent(message)
	ui.render()
	for len(actions) > 0 {
		if action := <-actions; action.Type == "RESOLVE_MESSAGE_TARGET" {
			t.Fatal("unchanged author metadata re-resolved the stationary draft")
		}
	}
	message.RecipientUnchanged = false // new/renamed/moved/evicted identity
	ui.applyEvent(message)
	ui.render()
	for len(actions) > 0 {
		if action := <-actions; action.Type == "RESOLVE_MESSAGE_TARGET" {
			return
		}
	}
	t.Fatal("changed recipient metadata did not refresh the stationary draft")
}
