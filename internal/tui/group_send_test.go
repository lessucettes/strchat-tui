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

func TestActionInputUsesNormalSend(t *testing.T) {
	s := newSimTUI(t, 120, 32)
	s.onLoop(func() {
		s.ui.applyEvent(focusGroupState())
		s.ui.input.SetText("/me waves 👋")
	})
	s.key(tcell.KeyEnter, 0, tcell.ModNone)
	action := takeSend(t, s)
	if action.Payload != "/me waves 👋" || action.View != "mixed" || action.Chat != "coffee" || action.ExpectedChat != "coffee" {
		t.Fatalf("action changed the message or destination: %+v", action)
	}
	s.onLoop(func() {
		if s.ui.input.GetText() != "" {
			t.Error("submitted action remained in the input")
		}
		s.ui.applyEvent(client.DisplayEvent{Type: "NEW_MESSAGE", Chat: "coffee", Nick: "alice",
			ShortPubKey: "abcd", Content: action.Payload, IsOwnMessage: true})
	})
	s.draw()
	if text := s.text(); !strings.Contains(text, "* alice#abcd waves 👋") {
		t.Fatalf("action was not rendered on screen: %s", text)
	}
	// Like ordinary messages, a full action queue must preserve the draft.
	s.onLoop(func() {
		for len(s.actions) < cap(s.actions) {
			s.actions <- client.UserAction{Type: "LIST_CHATS"}
		}
		s.ui.input.SetText("/me retries")
	})
	s.key(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, time.Second, func() bool {
		return s.matches(func() bool { return !s.ui.lastBell.IsZero() })
	})
	if text := onLoopValue(s, func() string { return s.ui.input.GetText() }); text != "/me retries" {
		t.Fatalf("backpressure discarded action draft: %q", text)
	}
	s.onLoop(func() {
		for len(s.actions) > 0 {
			<-s.actions
		}
		s.ui.input.SetText("/me")
	})
	s.key(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, time.Second, func() bool {
		return s.matches(func() bool { return s.ui.input.GetText() == "" })
	})
	s.draw()
	s.onLoop(func() {
		for len(s.actions) > 0 {
			if action := <-s.actions; action.Type == "SEND_MESSAGE" {
				t.Error("empty action was sent")
			}
		}
		if logs := s.ui.logsView.GetText(true); !strings.Contains(logs, "Usage: /me <action>") {
			t.Error("empty action did not show usage")
		}
	})
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
		})
	}
}
