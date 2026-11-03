package tui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/lessucettes/strchat-tui/internal/client"
	"github.com/rivo/tview"
)

func TestNicknameCompletionPreservesDraftPrefix(t *testing.T) {
	s := newSimTUI(t, 120, 30)
	for _, prefix := range []string{"", "Hello, ", "/me waves to ", "@bob#1234 hello ", "Привет, "} {
		s.onLoop(func() {
			s.ui.input.SetText("@bob#1234 ") // A completed recipient ends the previous query.
			s.ui.input.SetText(prefix + "@ali")
			select {
			case action := <-s.actions:
				if action.Type != "REQUEST_NICK_COMPLETION" || action.Payload != "ali" {
					t.Errorf("unexpected completion request: %+v", action)
				}
			default:
				t.Errorf("no completion request for %q", prefix+"@ali")
			}
			s.ui.applyEvent(client.DisplayEvent{Type: "NICK_COMPLETION_RESULT", Payload: []string{"@alice#abcd "}})
			s.ui.render()
			event := s.ui.handleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
			if event != nil {
				s.ui.input.InputHandler()(event, s.ui.setFocus)
			}
		})
		if got := onLoopValue(s, func() string { return s.ui.input.GetText() }); got != prefix+"@alice#abcd " {
			t.Errorf("completion lost draft: got %q, want %q", got, prefix+"@alice#abcd ")
		}
		for len(s.actions) > 0 {
			if action := <-s.actions; action.Type == "SEND_MESSAGE" {
				t.Fatalf("completion prematurely sent %+v", action)
			}
		}
	}
}

func TestNicknameCompletionNavigationAndMousePreserveLiteralDraft(t *testing.T) {
	for _, mode := range []string{"arrows", "mouse", "long draft"} {
		t.Run(mode, func(t *testing.T) {
			s := newSimTUI(t, 80, 24)
			prefix := "see [red] [#00ff00] "
			if mode == "long draft" {
				prefix += strings.Repeat("long draft ", 10)
			}
			s.onLoop(func() {
				s.ui.input.SetText(prefix + "@ali")
				s.ui.applyEvent(client.DisplayEvent{Type: "NICK_COMPLETION_RESULT", Payload: []string{"@alice#abcd ", "@alina#ef01 "}})
				s.ui.render()
			})
			s.draw()
			x, y := s.findByPrefix("@alice#abcd")
			if x < 0 {
				t.Fatalf("candidate nickname is not visible in the popup:\n%s", s.text())
			}
			s.onLoop(func() {
				press := func(key tcell.Key) {
					if event := s.ui.handleKey(tcell.NewEventKey(key, 0, tcell.ModNone)); event != nil {
						s.ui.input.InputHandler()(event, s.ui.setFocus)
					}
				}
				want := prefix + "@alina#ef01 "
				if mode == "mouse" {
					want = prefix + "@alice#abcd "
					s.ui.input.MouseHandler()(tview.MouseLeftClick, tcell.NewEventMouse(x, y, tcell.Button1, tcell.ModNone), s.ui.setFocus)
				} else {
					press(tcell.KeyDown)
					if got := s.ui.input.GetText(); got != prefix+"@alina#ef01 " {
						t.Errorf("navigation damaged draft: %q", got)
					}
					press(tcell.KeyUp)
					press(tcell.KeyDown)
					press(tcell.KeyEnter)
				}
				if got := s.ui.input.GetText(); got != want {
					t.Errorf("selection damaged draft: %q", got)
				}
			})
			for len(s.actions) > 0 {
				if action := <-s.actions; action.Type == "SEND_MESSAGE" {
					t.Fatal("selecting completion sent a message")
				}
			}
		})
	}
}

func TestNicknameCompletionRejectsOversizedReplacement(t *testing.T) {
	s := newSimTUI(t, 80, 24)
	draft := strings.Repeat("x", client.MaxMsgLen-5) + " @ali"
	s.onLoop(func() {
		s.ui.input.SetText(draft)
		s.ui.applyEvent(client.DisplayEvent{Type: "NICK_COMPLETION_RESULT", Payload: []string{"@alice#abcd "}})
		s.ui.render()
		s.ui.input.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), s.ui.setFocus)
		if got := s.ui.input.GetText(); got != draft {
			t.Error("oversized completion replaced the valid draft")
		}
	})
}

func TestHistoryRecallDiscardsPreviousDraftCompletion(t *testing.T) {
	for _, key := range []tcell.Key{tcell.KeyCtrlP, tcell.KeyCtrlN} {
		s := newSimTUI(t, 80, 24)
		s.onLoop(func() {
			press := func(key tcell.Key) {
				s.ui.input.InputHandler()(tcell.NewEventKey(key, 0, tcell.ModNone), s.ui.setFocus)
			}
			want := "recalled draft"
			s.ui.rememberInput(want)
			if key == tcell.KeyCtrlN {
				want = "restored draft"
				s.ui.input.SetText(want)
				press(tcell.KeyCtrlP)
			}
			s.ui.input.SetText("old @ali")
			s.ui.applyEvent(client.DisplayEvent{Type: "NICK_COMPLETION_RESULT", Payload: []string{"@alice#abcd "}})
			s.ui.render()
			press(key)
			press(tcell.KeyEnter)
			var sent string
			for len(s.actions) > 0 {
				if action := <-s.actions; action.Type == "SEND_MESSAGE" {
					sent = action.Payload
				}
			}
			if sent != want {
				t.Errorf("history key %v: sent %q, input %q; want %q", key, sent, s.ui.input.GetText(), want)
			}
		})
	}
}

func TestFullKeyRecipientCompletesNickname(t *testing.T) {
	identifier := "alice#" + strings.Repeat("a", 64)
	for _, trailing := range []string{"", " ", " hello"} {
		nick, complete := extractNickPrefix("@" + identifier + trailing)
		if !complete || nick != identifier {
			t.Fatalf("full-key recipient not complete: nick=%q complete=%t", nick, complete)
		}
	}
	ui := newIdleTUI(t, 1)
	ui.completionEntries = []string{"@" + identifier + " "}
	if entries := ui.handleAutocomplete("@" + identifier + " hello"); len(entries) != 0 {
		t.Fatal("completion remained active after selecting a full-key recipient")
	}
}
