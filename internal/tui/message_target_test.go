package tui

import (
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/lessucettes/strchat-tui/internal/client"
)

func TestGroupMentionPreviewResolvesActualDestination(t *testing.T) {
	s := newSimTUI(t, 120, 32)
	s.onLoop(func() {
		state := focusGroupState().Payload.(client.StateUpdate)
		state.ChatNicks = map[string]string{"coffee": "coffee-self", "ucfv0": "geo-self", "lobby": "lobby-self"}
		s.ui.applyEvent(client.DisplayEvent{Type: "STATE_UPDATE", Payload: state})
		s.ui.render()
		s.ui.openInfo()
		s.ui.handleInfoKeys(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
		if label := s.ui.input.GetLabel(); label != "geo-self > " {
			t.Errorf("choosing a member did not update the sender label: %q", label)
		}
		s.ui.handleInfoKeys(tcell.NewEventKey(tcell.KeyHome, 0, tcell.ModNone))
		if label := s.ui.input.GetLabel(); label != "coffee-self > " {
			t.Errorf("returning to coffee did not restore its sender label: %q", label)
		}
		s.ui.setFocus(s.ui.input)
		s.ui.input.SetText("@bob#beef hello")
		s.ui.render()
	})
	var request client.UserAction
	deadline := time.After(time.Second)
preview:
	for {
		select {
		case action := <-s.actions:
			if action.Type == "RESOLVE_MESSAGE_TARGET" {
				request = action
				break preview
			}
		case <-deadline:
			t.Fatal("composer did not request an authoritative destination preview")
		}
	}
	if request.View != "mixed" || request.Chat != "coffee" || request.Payload != "@bob#beef hello" {
		t.Fatalf("incorrect preview context: %+v", request)
	}
	s.events <- client.DisplayEvent{Type: "MESSAGE_TARGET", Payload: client.MessageTarget{
		Text: request.Payload, View: request.View, DefaultChat: request.Chat, Chat: "ucfv0",
	}}
	waitFor(t, time.Second, func() bool {
		return s.matches(func() bool {
			target, ready := s.ui.currentMessageTarget(s.ui.snapshot())
			return ready && target.Error == "" && target.Chat == "ucfv0"
		})
	})
	s.draw()
	if label := onLoopValue(s, func() string { return s.ui.input.GetLabel() }); label != "geo-self > " {
		t.Fatalf("mention preview did not show the actual sending identity: %q", label)
	}
	if got := onLoopValue(s, func() string { return s.ui.conversationChat(s.ui.snapshot()) }); got != "coffee" {
		t.Fatal("mention preview changed the reading focus")
	}
	s.onLoop(func() { s.ui.input.SetText("@someone#cafe next"); s.ui.render() })
	s.events <- client.DisplayEvent{Type: "MESSAGE_TARGET", Payload: client.MessageTarget{
		Text: request.Payload, View: request.View, DefaultChat: request.Chat, Chat: "ucfv0",
	}}
	s.draw()
	if ready := onLoopValue(s, func() bool { _, ready := s.ui.currentMessageTarget(s.ui.snapshot()); return ready }); ready {
		t.Fatal("stale preview was accepted for the current draft")
	}
	if label := onLoopValue(s, func() string { return s.ui.input.GetLabel() }); label != "> " {
		t.Fatalf("an unresolved mention retained a stale sender label: %q", label)
	}
}
