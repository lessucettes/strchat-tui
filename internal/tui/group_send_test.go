package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/lessucettes/strchat-tui/internal/client"
)

func takeSend(t *testing.T, s *simTUI) client.UserAction {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		select {
		case action := <-s.actions:
			if action.Type == "SEND_MESSAGE" {
				return action
			}
		case <-deadline.C:
			t.Fatal("no send action")
		}
	}
}

func TestGroupEnterCapturesChosenDestination(t *testing.T) {
	s := newSimTUI(t, 120, 32)
	s.onLoop(func() {
		s.ui.applyEvent(focusGroupState())
		s.ui.render()
		s.ui.openInfo()
		s.ui.handleInfoKeys(tcell.NewEventKey(tcell.KeyHome, 0, tcell.ModNone))
		s.ui.setFocus(s.ui.input)
		s.ui.input.SetText("hello")
	})
	s.key(tcell.KeyEnter, 0, tcell.ModNone)
	action := takeSend(t, s)
	if action.Payload != "hello" || action.View != "mixed" || action.Chat != "coffee" {
		t.Fatalf("send did not capture the active group and member: %+v", action)
	}
}

func TestGroupEnterUsesInitialDestination(t *testing.T) {
	for _, size := range [][2]int{{120, 32}, {80, 32}, {80, 14}, {58, 10}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			s := newSimTUI(t, size[0], size[1])
			s.onLoop(func() {
				s.ui.applyEvent(focusGroupState())
				s.ui.input.SetText("first message")
			})
			s.key(tcell.KeyEnter, 0, tcell.ModNone)
			action := takeSend(t, s)
			if action.Payload != "first message" || action.View != "mixed" || action.Chat != "coffee" {
				t.Fatalf("Enter without visiting Info did not use the first member: %+v", action)
			}
			if text := onLoopValue(s, func() string { return s.ui.input.GetText() }); text != "" {
				t.Fatalf("submitted initial message remained in the field: %q", text)
			}
			if title := onLoopValue(s, func() string { return s.ui.input.GetTitle() }); strings.Contains(title, "Enter") {
				t.Fatalf("composer title contains an instruction: %q", title)
			}
		})
	}
}
