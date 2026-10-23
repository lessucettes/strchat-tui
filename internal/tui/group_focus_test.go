package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/lessucettes/strchat-tui/internal/client"
)

func focusGroupState() client.DisplayEvent {
	return client.DisplayEvent{Type: "STATE_UPDATE", Payload: client.StateUpdate{
		Views: []client.View{
			{Name: "mixed", IsGroup: true, Children: []string{"coffee", "ucfv0", "lobby"}},
			{Name: "coffee"}, {Name: "ucfv0"}, {Name: "lobby"},
		}, ActiveViewIndex: 0, ChatNicks: map[string]string{"coffee": "me", "ucfv0": "me", "lobby": "me"},
	}}
}

func TestGroupInfoArrowsChooseConversation(t *testing.T) {
	s := newSimTUI(t, 120, 32)
	s.events <- focusGroupState()
	waitFor(t, time.Second, func() bool {
		return s.matches(func() bool { return len(s.ui.snapshot().views) == 4 })
	})
	s.draw()
	s.key(tcell.KeyRune, 'n', tcell.ModAlt)
	waitFor(t, time.Second, func() bool { return s.matches(func() bool { return s.ui.focused == s.ui.detailsView }) })
	waitFor(t, time.Second, func() bool {
		return s.matches(func() bool { return strings.Contains(s.ui.detailsView.GetText(true), "▶ coffee") })
	})
	s.key(tcell.KeyDown, 0, tcell.ModNone)
	waitFor(t, time.Second, func() bool {
		return s.matches(func() bool { return s.ui.conversationChat(s.ui.snapshot()) == "ucfv0" })
	})
	s.key(tcell.KeyRune, 'i', tcell.ModAlt)
	waitFor(t, time.Second, func() bool { return s.matches(func() bool { return s.ui.focused == s.ui.input }) })
	s.draw()
	if got := onLoopValue(s, func() string { return s.ui.detailsView.GetText(true) }); !strings.Contains(got, "▶ ucfv0") {
		t.Fatalf("conversation marker disappeared on returning to input: %q", got)
	}
	if active := onLoopValue(s, func() int { return s.ui.snapshot().activeIndex }); active != 0 {
		t.Fatal("choosing a conversation activated a different view")
	}
	for len(s.actions) > 0 {
		if action := <-s.actions; action.Type == "ACTIVATE_VIEW" {
			t.Fatal("choosing a conversation must not change subscriptions")
		}
	}
}

func TestGroupInfoTemporaryAccessOnTinyScreens(t *testing.T) {
	for _, size := range [][2]int{{58, 10}, {80, 14}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			s := newSimTUI(t, size[0], size[1])
			s.events <- focusGroupState()
			waitFor(t, time.Second, func() bool {
				return s.matches(func() bool { return len(s.ui.snapshot().views) == 4 })
			})
			s.onLoop(func() { s.ui.input.SetText("draft") })
			s.key(tcell.KeyRune, 'n', tcell.ModAlt)
			waitFor(t, time.Second, func() bool {
				return s.matches(func() bool { return s.ui.visible(s.ui.detailsView) && s.ui.focused == s.ui.detailsView })
			})
			s.key(tcell.KeyDown, 0, tcell.ModNone)
			waitFor(t, time.Second, func() bool {
				return s.matches(func() bool { return s.ui.conversationChat(s.ui.snapshot()) == "ucfv0" })
			})
			s.key(tcell.KeyRune, 'i', tcell.ModAlt)
			waitFor(t, time.Second, func() bool {
				return s.matches(func() bool { return s.ui.visible(s.ui.outputView) && !s.ui.visible(s.ui.detailsView) })
			})
			if draft := onLoopValue(s, func() string { return s.ui.input.GetText() }); draft != "draft" {
				t.Fatalf("temporary Info changed draft: %q", draft)
			}
		})
	}
}
