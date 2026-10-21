package tui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/lessucettes/strchat-tui/internal/client"
)

func TestGroupRecipientPreviewRefreshesAfterMembershipChange(t *testing.T) {
	actions := make(chan client.UserAction, 64)
	ui := New(actions, make(chan client.DisplayEvent))
	t.Cleanup(ui.restoreLog)
	ui.applyEvent(focusGroupState())
	ui.render()
	ui.openInfo()
	ui.handleInfoKeys(tcell.NewEventKey(tcell.KeyHome, 0, tcell.ModNone))
	const text = "@peer#beef hi"
	ui.input.SetText(text)
	ui.render()
	for len(actions) > 0 {
		<-actions
	}
	ui.applyEvent(client.DisplayEvent{Type: "MESSAGE_TARGET", Payload: client.MessageTarget{
		Text: text, View: "mixed", DefaultChat: "coffee", Chat: "lobby",
	}})
	ui.render()
	if target, ready := ui.currentMessageTarget(ui.snapshot()); !ready || target.Chat != "lobby" {
		t.Fatal("fixture did not preview the mentioned chat")
	}
	// Coffee remains selected, so the composer text and default destination
	// do not change. Membership alone must invalidate the old resolution.
	state := focusGroupState().Payload.(client.StateUpdate)
	state.Views[0].Children = []string{"coffee", "ucfv0"}
	state.Views = state.Views[:3]
	ui.applyEvent(client.DisplayEvent{Type: "STATE_UPDATE", Payload: state})
	ui.render()
	refreshed := false
	for len(actions) > 0 {
		action := <-actions
		if action.Type == "RESOLVE_MESSAGE_TARGET" && action.Payload == text && action.View == "mixed" && action.Chat == "coffee" {
			refreshed = true
		}
	}
	if !refreshed {
		t.Fatal("membership change did not re-resolve the unchanged mention")
	}
	const refusal = "Unknown recipient. Use nickname completion."
	ui.applyEvent(client.DisplayEvent{Type: "MESSAGE_TARGET", Payload: client.MessageTarget{
		Text: text, View: "mixed", DefaultChat: "coffee", Error: refusal,
	}})
	ui.render()
	ui.input.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), func(tview.Primitive) {})
	if ui.input.GetText() != text {
		t.Fatal("refused mention discarded the draft")
	}
	if logs := strings.Join(ui.logs.snapshot(), "\n"); !strings.Contains(logs, refusal) {
		t.Fatalf("Enter refusal was silent: %q", logs)
	}
	for len(actions) > 0 {
		if action := <-actions; action.Type == "SEND_MESSAGE" {
			t.Fatal("removed recipient was submitted")
		}
	}
}
