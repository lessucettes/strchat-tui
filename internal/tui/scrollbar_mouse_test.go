package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/lessucettes/strchat-tui/internal/client"
)

func TestScrollbarTrackAndDragNeverRouteChats(t *testing.T) {
	for _, mode := range []client.ScrollbarPosition{1, 2} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			s := newSimTUI(t, 120, 32)
			fillScrollbarPanes(s)
			s.onLoop(func() {
				s.ui.applyEvent(client.DisplayEvent{Type: "SCROLLBAR_UPDATE", Payload: mode})
				s.ui.input.SetText("untouched draft")
			})
			s.draw()
			// Chats comes last: its local preview cursor may change while scrolling,
			// but activation and the active group's send destination must not.
			for _, pane := range []scrollbarTestView{s.ui.outputView, s.ui.logsView, s.ui.detailsView, s.ui.chatList} {
				if pane == s.ui.logsView {
					s.onLoop(func() { s.ui.setMaximized(maximizeLogs) })
					s.draw()
				}
				point := onLoopValue(s, func() [2]int {
					x, y, w, h := pane.GetRect()
					if mode == 1 {
						x += w - 1
					}
					return [2]int{x, y + h - 2}
				})
				s.onLoop(func() {
					switch view := pane.(type) {
					case *scrollbackView:
						view.ScrollToBeginning()
					case *tview.List:
						view.SetCurrentItem(0).SetOffset(0, 0)
					case *tview.TextView:
						view.ScrollToBeginning()
					}
				})
				s.draw()
				clickMouse(s, point[0], point[1])
				waitFor(t, time.Second, func() bool {
					return s.matches(func() bool {
						if s.ui.scrollbarPress {
							return false // the track click's release must also be processed
						}
						switch view := pane.(type) {
						case *scrollbackView:
							row, _ := view.GetScrollOffset()
							return row > 0
						case *tview.List:
							row, _ := view.GetOffset()
							return row > 0
						case *tview.TextView:
							row, _ := view.GetScrollOffset()
							return row > 0
						}
						return false
					})
				})
				s.draw()
				thumb := onLoopValue(s, func() [2]int {
					_, y, _, h := pane.GetRect()
					for row := y + 1; row < y+h-1; row++ {
						if r, _, _, _ := s.screen.GetContent(point[0], row); r == '█' {
							return [2]int{point[0], row}
						}
					}
					return [2]int{-1, -1}
				})
				if thumb[0] < 0 {
					t.Fatal("no draggable thumb rendered")
				}
				s.screen.PostEventWait(tcell.NewEventMouse(thumb[0], thumb[1], tcell.Button1, tcell.ModNone))
				waitFor(t, time.Second, func() bool { return s.matches(func() bool { return s.ui.scrollbarDrag != nil }) })
				// Move horizontally outside the pane while dragging to its bottom.
				s.screen.PostEventWait(tcell.NewEventMouse(119, point[1], tcell.Button1, tcell.ModNone))
				s.screen.PostEventWait(tcell.NewEventMouse(20, 31, tcell.ButtonNone, tcell.ModNone))
				waitFor(t, time.Second, func() bool { return s.matches(func() bool { return !s.ui.scrollbarPress }) })
				s.draw()
				if bottom := onLoopValue(s, func() rune { r, _, _, _ := s.screen.GetContent(point[0], point[1]); return r }); bottom != '█' {
					t.Fatalf("drag did not reach the end: last track cell %q", bottom)
				}
				if pane == s.ui.detailsView {
					s.onLoop(func() {
						s.ui.applyEvent(client.DisplayEvent{Type: "RELAYS_UPDATE", Payload: []client.RelayInfo{{URL: "ws://127.0.0.1:1"}}})
					})
					s.draw()
					if bottom := onLoopValue(s, func() rune { r, _, _, _ := s.screen.GetContent(point[0], point[1]); return r }); bottom != '█' {
						t.Fatal("unrelated relay status reset the scrolled group members")
					}
				}
				if pane == s.ui.logsView {
					s.onLoop(func() { s.ui.setMaximized(maximizeNone); s.ui.setFocus(s.ui.input) })
					s.draw()
				}
			}
			if !s.matches(func() bool {
				return s.ui.input.GetText() == "untouched draft" && s.ui.conversationChat(s.ui.snapshot()) == "chat" && s.ui.focused == s.ui.input
			}) || len(s.actions) != 0 {
				t.Fatal("scrollbar interaction changed focus/draft/destination or activated a chat")
			}
		})
	}
}

func TestScrollbarCancelledDragAndCompactLogs(t *testing.T) {
	s := newSimTUI(t, 120, 32)
	fillScrollbarPanes(s)
	s.onLoop(func() {
		s.ui.applyEvent(client.DisplayEvent{Type: "SCROLLBAR_UPDATE", Payload: client.ScrollbarRight})
		s.ui.setFocus(s.ui.logsView)
	})
	s.draw()
	s.onLoop(func() {
		if borderHasThumb(s, s.ui.logsView, false) || borderHasThumb(s, s.ui.logsView, true) {
			t.Error("compact Logs must not have a scrollbar")
		}
		if !strings.Contains(s.ui.hints.GetText(true), "Maximize | ↑/↓: Scroll") {
			t.Error("Logs must advertise Scroll immediately after Maximize")
		}
	})
	point := onLoopValue(s, func() [2]int {
		x, y, w, _ := s.ui.detailsView.GetRect()
		return [2]int{x + w - 1, y + 1}
	})
	s.screen.PostEventWait(tcell.NewEventMouse(point[0], point[1], tcell.Button1, tcell.ModNone))
	waitFor(t, time.Second, func() bool { return s.matches(func() bool { return s.ui.scrollbarDrag != nil }) })
	// Turning the bar off mid-drag must consume the remaining move/release,
	// not hand the held press to a chat underneath it.
	s.onLoop(func() { s.ui.applyEvent(client.DisplayEvent{Type: "SCROLLBAR_UPDATE", Payload: client.ScrollbarOff}) })
	s.draw()
	s.screen.PostEventWait(tcell.NewEventMouse(100, 5, tcell.Button1, tcell.ModNone))
	s.screen.PostEventWait(tcell.NewEventMouse(100, 5, tcell.ButtonNone, tcell.ModNone))
	waitFor(t, time.Second, func() bool { return s.matches(func() bool { return !s.ui.scrollbarPress }) })
	if !s.matches(func() bool { return s.ui.focused == s.ui.logsView }) || len(s.actions) != 0 {
		t.Fatal("disabled scrollbar released its captured press into Chats")
	}
	// A resize can hide the pane mid-drag. Its previous geometry must not
	// remain a live hit target in the new layout.
	s.onLoop(func() { s.ui.applyEvent(client.DisplayEvent{Type: "SCROLLBAR_UPDATE", Payload: client.ScrollbarLeft}) })
	s.draw()
	point = onLoopValue(s, func() [2]int {
		x, y, _, _ := s.ui.detailsView.GetRect()
		return [2]int{x, y + 1}
	})
	s.screen.PostEventWait(tcell.NewEventMouse(point[0], point[1], tcell.Button1, tcell.ModNone))
	waitFor(t, time.Second, func() bool { return s.matches(func() bool { return s.ui.scrollbarPress }) })
	s.onLoop(func() { s.screen.SetSize(58, 10) })
	s.draw()
	s.screen.PostEventWait(tcell.NewEventMouse(20, 4, tcell.Button1, tcell.ModNone))
	s.screen.PostEventWait(tcell.NewEventMouse(20, 4, tcell.ButtonNone, tcell.ModNone))
	waitFor(t, time.Second, func() bool { return s.matches(func() bool { return !s.ui.scrollbarPress }) })
	if len(s.actions) != 0 || !s.matches(func() bool { return s.ui.conversationChat(s.ui.snapshot()) == "chat" }) {
		t.Fatal("resizing a scrollbar drag rerouted the conversation")
	}
}
