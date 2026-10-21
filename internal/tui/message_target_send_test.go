package tui

import (
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/lessucettes/strchat-tui/internal/client"
)

func TestGroupMentionSendWaitsForResolvedDestination(t *testing.T) {
	for _, ready := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "resolved"}[ready], func(t *testing.T) {
			s := newSimTUI(t, 120, 32)
			s.onLoop(func() {
				s.ui.applyEvent(focusGroupState())
				s.ui.render()
				s.ui.openInfo()
				s.ui.handleInfoKeys(tcell.NewEventKey(tcell.KeyHome, 0, tcell.ModNone))
				s.ui.setFocus(s.ui.input)
				s.ui.input.SetText("@bob#beef hello")
				if ready {
					s.ui.applyEvent(client.DisplayEvent{Type: "MESSAGE_TARGET", Payload: client.MessageTarget{
						Text: "@bob#beef hello", View: "mixed", DefaultChat: "coffee", Chat: "ucfv0",
					}})
				}
				s.ui.render()
			})
			s.key(tcell.KeyEnter, 0, tcell.ModNone)
			if ready {
				action := takeSend(t, s)
				if action.ExpectedChat != "ucfv0" || action.Chat != "coffee" {
					t.Fatalf("send did not bind the destination shown by the preview: %+v", action)
				}
				return
			}
			waitFor(t, time.Second, func() bool {
				return s.matches(func() bool { return !s.ui.lastBell.IsZero() || s.ui.input.GetText() == "" })
			})
			if text := onLoopValue(s, func() string { return s.ui.input.GetText() }); text != "@bob#beef hello" {
				t.Fatal("pending recipient resolution lost the draft")
			}
			for len(s.actions) > 0 {
				if action := <-s.actions; action.Type == "SEND_MESSAGE" {
					t.Fatal("send was submitted without a known destination")
				}
			}
		})
	}
}
