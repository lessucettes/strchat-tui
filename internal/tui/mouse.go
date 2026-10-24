package tui

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/lessucettes/strchat-tui/internal/client"
)

func (t *tui) setupMouse() {
	completionPress := false
	t.app.SetMouseCapture(func(event *tcell.EventMouse, action tview.MouseAction) (*tcell.EventMouse, tview.MouseAction) {
		if event == nil {
			return nil, action
		}
		if t.scrollbarPress && t.captureScrollbar(event, action) {
			return nil, action
		}
		if action == tview.MouseLeftUp && completionPress {
			completionPress = false
			return nil, action
		}
		if t.focused == t.input && t.input.HasFocus() {
			inputAction := action
			switch action {
			case tview.MouseLeftDown:
				// The completion popup overpaints sibling panes. Let the
				// input accept it on press, before another pane can blur it;
				// swallow the release so it cannot activate the row below.
				inputAction = tview.MouseLeftClick
				fallthrough
			case tview.MouseScrollUp, tview.MouseScrollDown, tview.MouseScrollLeft, tview.MouseScrollRight:
				if consumed, _ := t.input.MouseHandler()(inputAction, event, func(tview.Primitive) {}); consumed {
					if action == tview.MouseLeftDown {
						completionPress = true
					}
					return nil, action
				}
			}
		}
		if t.captureScrollbar(event, action) {
			return nil, action
		}
		if action == tview.MouseLeftDown {
			x, y := event.Position()
			for _, pane := range []tview.Primitive{t.input, t.chatList, t.detailsView, t.outputView, t.logsView} {
				left, top, width, height := pane.GetRect()
				if t.visible(pane) && x >= left && x < left+width && y >= top && y < top+height {
					if t.focused != pane {
						t.setFocus(pane)
					}
					break
				}
			}
		}
		// tview gives a rapid second click a different action; lists should
		// activate the row under either click, not ignore the second one.
		if action == tview.MouseLeftDoubleClick && t.visible(t.chatList) && t.chatList.InRect(event.Position()) {
			action = tview.MouseLeftClick
		}
		return event, action
	})
	t.chatList.SetSelectedFunc(func(index int, _, _ string, _ rune) {
		s := t.snapshot()
		if index >= 0 && index < len(s.views) {
			// A click activates one view, never creates a group from marked rows.
			t.submit(client.UserAction{Type: "ACTIVATE_VIEW", Payload: s.views[index].Name})
		}
	})
	t.detailsView.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		if action != tview.MouseLeftClick && action != tview.MouseLeftDoubleClick {
			return action, event
		}
		if !t.detailsView.InInnerRect(event.Position()) {
			return action, event
		}
		s := t.snapshot()
		view := activeView(s)
		if view == nil || !view.IsGroup || t.chatList.GetCurrentItem() != s.activeIndex {
			return action, event
		}
		_, y := event.Position()
		_, top, width, _ := t.detailsView.GetInnerRect()
		offset, _ := t.detailsView.GetScrollOffset()
		row := y - top + offset
		for _, chat := range view.Children {
			lines := groupInfoLines(chat, width)
			if row < lines {
				t.selectConversation(s, chat)
				return tview.MouseConsumed, nil
			}
			row -= lines
		}
		return action, event
	})
}

// Match Info's word wrapping, including the three-cell selection prefix.
func groupInfoLines(chat string, width int) int {
	return max(1, len(tview.WordWrap(" - "+sanitizeLineForDisplay(chat), max(1, width))))
}
