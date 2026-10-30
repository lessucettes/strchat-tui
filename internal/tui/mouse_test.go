package tui

import (
	"fmt"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/lessucettes/strchat-tui/internal/client"
)

func clickMouse(s *simTUI, x, y int) {
	s.t.Helper()
	s.screen.PostEventWait(tcell.NewEventMouse(x, y, tcell.Button1, tcell.ModNone))
	s.screen.PostEventWait(tcell.NewEventMouse(x, y, tcell.ButtonNone, tcell.ModNone))
}

func TestMouseFocusAndWheelPreserveKeyboardNavigation(t *testing.T) {
	s := newSimTUI(t, 120, 32)
	s.onLoop(func() {
		s.ui.applyEvent(testState(client.View{Name: "chat"}, client.View{Name: "other"}))
		s.ui.selectedForGroup["chat"], s.ui.selectedForGroup["other"] = true, true
		for i := range 80 {
			s.ui.applyEvent(testMessage(i))
		}
	})
	s.draw()
	for _, pane := range []tview.Primitive{s.ui.outputView, s.ui.chatList, s.ui.detailsView, s.ui.logsView, s.ui.input} {
		point := onLoopValue(s, func() [2]int {
			x, y, _, _ := pane.GetRect()
			return [2]int{x + 2, y + 1}
		})
		clickMouse(s, point[0], point[1])
		waitFor(t, time.Second, func() bool { return s.matches(func() bool { return s.ui.focused == pane }) })
		if pane == s.ui.chatList {
			waitFor(t, time.Second, func() bool { return len(s.actions) != 0 })
			if action := <-s.actions; action.Type != "ACTIVATE_VIEW" || action.Payload != "chat" {
				t.Fatalf("clicking a marked row must activate only that view: %+v", action)
			}
		}
	}
	s.onLoop(func() { s.ui.outputView.ScrollTo(15, 0) })
	s.draw()
	point := onLoopValue(s, func() [2]int {
		x, y, _, _ := s.ui.outputView.GetInnerRect()
		return [2]int{x, y}
	})
	s.screen.PostEventWait(tcell.NewEventMouse(point[0], point[1], tcell.WheelUp, tcell.ModNone))
	waitFor(t, time.Second, func() bool {
		return s.matches(func() bool { row, _ := s.ui.outputView.GetScrollOffset(); return row < 15 })
	})
	if !s.matches(func() bool { return s.ui.focused == s.ui.input }) {
		t.Fatal("scrolling a pane stole keyboard focus")
	}
	s.key(tcell.KeyTab, 0, tcell.ModNone)
	waitFor(t, time.Second, func() bool { return s.matches(func() bool { return s.ui.focused == s.ui.chatList }) })
}

func TestMouseGroupDestinationUsesWrappedRowsAndPreservesDraft(t *testing.T) {
	for _, size := range [][2]int{{120, 32}, {58, 10}, {16, 10}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			s := newSimTUI(t, size[0], size[1])
			// The count prefix makes this row wrap where the old "- " prefix
			// did not, so hit testing must measure the rendered text.
			chat := "qqqqqqqqqq"
			s.onLoop(func() {
				s.ui.applyEvent(testState(client.View{Name: "mixed", IsGroup: true, Children: []string{"coffee", chat, "lobby"}}))
				s.ui.applyEvent(client.DisplayEvent{Type: "PRESENCE_UPDATE", Payload: map[string]int{chat: 125}})
				for i := range 80 {
					event := testMessage(i)
					event.Chat = "coffee"
					s.ui.applyEvent(event)
				}
				s.ui.input.SetText("draft")
			})
			s.draw()
			s.onLoop(func() { s.ui.outputView.ScrollTo(5, 0) })
			s.key(tcell.KeyRune, 'n', tcell.ModAlt)
			waitFor(t, time.Second, func() bool { return s.matches(func() bool { return s.ui.focused == s.ui.detailsView }) })
			s.draw()
			point := onLoopValue(s, func() [2]int {
				x, y, width, height := s.ui.detailsView.GetInnerRect()
				point := [2]int{-1, -1}
				// Click the last visible part of the name, including a wrapped
				// continuation on a tiny terminal.
				for row := y; row < y+height; row++ {
					for col := x; col < x+width; col++ {
						r, _, _, _ := s.screen.GetContent(col, row)
						if r == 'q' {
							point = [2]int{col, row}
						}
					}
				}
				return point
			})
			if point[0] < 0 {
				t.Fatal("group member was not visible")
			}
			clickMouse(s, point[0], point[1])
			waitFor(t, time.Second, func() bool { return s.matches(func() bool { return s.ui.conversationChat(s.ui.snapshot()) == chat }) })
			point = onLoopValue(s, func() [2]int { x, y, _, _ := s.ui.input.GetRect(); return [2]int{x + 1, y} })
			clickMouse(s, point[0], point[1])
			waitFor(t, time.Second, func() bool {
				return s.matches(func() bool { return s.ui.focused == s.ui.input && !s.ui.infoTemporary })
			})
			s.draw()
			if row := onLoopValue(s, func() int { row, _ := s.ui.outputView.GetScrollOffset(); return row }); row != 5 {
				t.Fatalf("selecting a destination moved messages to row %d", row)
			}
			s.key(tcell.KeyEnter, 0, tcell.ModNone)
			action := takeSend(t, s)
			if action.Payload != "draft" || action.View != "mixed" || action.Chat != chat || action.ExpectedChat != chat {
				t.Fatalf("mouse selection changed the draft or sent to the wrong chat: %+v", action)
			}
		})
	}
}

func TestMouseCompletionPopupDoesNotActivateUnderlyingChat(t *testing.T) {
	for _, size := range [][2]int{{120, 32}, {90, 16}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			s := newSimTUI(t, size[0], size[1])
			s.onLoop(func() {
				s.ui.applyEvent(testState(client.View{Name: "alpha"}, client.View{Name: "bravo"}, client.View{Name: "charlie"}))
			})
			s.draw()
			s.onLoop(func() {
				s.ui.completionEntries = []string{"@nick-one#abcd ", "@nick-two#ef01 ", "@nick-three#2345 "}
				s.ui.input.SetText("@n")
				s.ui.input.Autocomplete()
			})
			s.draw()
			x, y := s.findByPrefix("@nick-one")
			if x < 0 {
				t.Fatal("completion popup was not visible")
			}
			clickMouse(s, x+1, y)
			waitFor(t, time.Second, func() bool {
				return s.matches(func() bool { return s.ui.input.GetText() == "@nick-one#abcd " && s.ui.focused == s.ui.input })
			})
			s.key(tcell.KeyRune, 'X', tcell.ModNone)
			waitFor(t, time.Second, func() bool {
				return s.matches(func() bool { return s.ui.input.GetText() == "@nick-one#abcd X" })
			})
			for len(s.actions) > 0 {
				if action := <-s.actions; action.Type == "ACTIVATE_VIEW" || action.Type == "SEND_MESSAGE" {
					t.Fatalf("completion click leaked to the pane underneath: %+v", action)
				}
			}
		})
	}
}

func TestMouseInfoDoesNotReroutePreviewOrRejectedSelection(t *testing.T) {
	for _, preview := range []bool{true, false} {
		t.Run(map[bool]string{true: "inactive preview", false: "full action queue"}[preview], func(t *testing.T) {
			s := newSimTUI(t, 120, 32)
			s.onLoop(func() {
				s.ui.applyEvent(testState(
					client.View{Name: "active", IsGroup: true, Children: []string{"coffee", "lobby"}},
					client.View{Name: "preview", IsGroup: true, Children: []string{"coffee", "lobby"}},
				))
				if !preview {
					s.ui.actionsChan = make(chan client.UserAction)
				}
			})
			s.draw()
			if preview {
				s.onLoop(func() { s.ui.chatList.SetCurrentItem(1) })
				s.draw()
			}
			point := onLoopValue(s, func() [2]int { x, y, _, _ := s.ui.detailsView.GetInnerRect(); return [2]int{x + 3, y + 1} })
			clickMouse(s, point[0], point[1])
			s.key(tcell.KeyTab, 0, tcell.ModNone)
			waitFor(t, time.Second, func() bool { return s.matches(func() bool { return s.ui.focused == s.ui.input }) })
			if chat := onLoopValue(s, func() string { return s.ui.conversationChat(s.ui.snapshot()) }); chat != "coffee" {
				t.Fatalf("rejected/preview click rerouted the active group to %q", chat)
			}
			if len(s.actions) != 0 {
				t.Fatal("preview click queued an action")
			}
		})
	}
}
