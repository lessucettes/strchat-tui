package tui

import (
	"fmt"
	"testing"

	"github.com/lessucettes/strchat-tui/internal/client"
)

func BenchmarkStationaryMentionTraffic(b *testing.B) {
	for _, unchanged := range []bool{false, true} {
		b.Run(fmt.Sprintf("unchanged=%v", unchanged), func(b *testing.B) {
			actions := make(chan client.UserAction, 64)
			ui := New(actions, nil)
			defer ui.restoreLog()
			ui.applyEvent(focusGroupState())
			ui.input.SetText("@peer#beef hi")
			ui.render()
			for len(actions) > 0 {
				<-actions
			}
			ev := testMessage(1)
			ev.Chat, ev.RecipientUnchanged = "coffee", unchanged
			requests := 0
			b.ReportAllocs()
			for b.Loop() {
				ui.applyEvent(ev)
				ui.render()
				for len(actions) > 0 {
					if (<-actions).Type == "RESOLVE_MESSAGE_TARGET" {
						requests++
					}
				}
			}
			b.ReportMetric(float64(requests)/float64(b.N), "preview-requests/op")
		})
	}
}

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
