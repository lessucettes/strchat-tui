package tui

import (
	"fmt"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/lessucettes/strchat-tui/internal/client"
)

type scrollbarTestView interface {
	tview.Primitive
	GetInnerRect() (int, int, int, int)
}

func fillScrollbarPanes(s *simTUI) {
	s.t.Helper()
	s.onLoop(func() {
		children := []string{"chat"}
		for i := range 49 {
			children = append(children, fmt.Sprintf("chat%02d", i))
		}
		views := []client.View{{Name: "mixed", IsGroup: true, Children: children, SelectedChat: "chat"}}
		for _, name := range children {
			views = append(views, client.View{Name: name})
		}
		s.ui.applyEvent(testState(views...))
		for i := range 80 {
			s.ui.applyEvent(testMessage(i))
			s.ui.applyEvent(client.DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("log entry %d", i)})
		}
	})
	s.draw()
}

func borderHasThumb(s *simTUI, pane tview.Primitive, left bool) bool {
	x, y, w, h := pane.GetRect()
	if !left {
		x += w - 1
	}
	for row := y + 1; row < y+h-1; row++ {
		if r, _, _, _ := s.screen.GetContent(x, row); r == '█' {
			return true
		}
	}
	return false
}

func TestScrollbarInfoWrappingResizeAndMonochrome(t *testing.T) {
	s := newSimTUI(t, 120, 32)
	fillScrollbarPanes(s)
	s.onLoop(func() {
		s.ui.applyEvent(client.DisplayEvent{Type: "THEME_UPDATE", Content: "monochrome"})
		s.ui.applyEvent(client.DisplayEvent{Type: "SCROLLBAR_UPDATE", Payload: client.ScrollbarLeft})
	})
	for _, size := range [][2]int{{120, 130}, {80, 32}, {16, 10}, {120, 32}} {
		s.onLoop(func() { s.screen.SetSize(size[0], size[1]) })
		s.draw()
		s.onLoop(func() { s.ui.handleKey(tcell.NewEventKey(tcell.KeyRune, 'n', tcell.ModAlt)) })
		s.draw()
		s.onLoop(func() {
			_, _, _, height := s.ui.detailsView.GetInnerRect()
			overflow := s.ui.detailsView.GetWrappedLineCount() > height
			if borderHasThumb(s, s.ui.detailsView, true) != overflow {
				t.Errorf("Info scrollbar disagrees with wrapped content after resize to %v", size)
			}
		})
	}
}

func TestScrollbarVisibilitySideAndUnchangedContents(t *testing.T) {
	// Toggling the bar must not reflow text or overwrite the group gutter.
	s := newSimTUI(t, 120, 32)
	s.onLoop(func() {
		s.ui.applyEvent(testState(client.View{Name: "chat"}))
		s.ui.applyEvent(client.DisplayEvent{Type: "SCROLLBAR_UPDATE", Payload: client.ScrollbarPosition(1)})
	})
	s.draw()
	panes := []scrollbarTestView{s.ui.outputView, s.ui.logsView, s.ui.chatList, s.ui.detailsView}
	if onLoopValue(s, func() bool {
		for _, pane := range panes {
			if borderHasThumb(s, pane, false) {
				return true
			}
		}
		return false
	}) {
		t.Fatal("scrollbar shown when the contents fit")
	}
	s.onLoop(func() {
		_, _, _, height := s.ui.detailsView.GetInnerRect()
		children := make([]string, height)
		for i := range children {
			children[i] = fmt.Sprintf("chat%02d", i)
		}
		s.ui.applyEvent(testState(client.View{Name: "exact", IsGroup: true, Children: children, SelectedChat: children[0]}))
	})
	s.draw()
	if s.matches(func() bool { return borderHasThumb(s, s.ui.detailsView, false) }) {
		t.Fatal("Info scrollbar shown for a synthetic empty line after exactly fitting members")
	}
	s.onLoop(func() {
		s.ui.applyEvent(client.DisplayEvent{Type: "SCROLLBAR_UPDATE", Payload: client.ScrollbarPosition(0)})
	})
	fillScrollbarPanes(s)
	before := onLoopValue(s, func() [][][]scrollbackCell {
		var contents [][][]scrollbackCell
		for _, pane := range panes {
			x, y, w, h := pane.GetInnerRect()
			contents = append(contents, scrollbackProjection(s.screen, x, y, w, h))
		}
		return contents
	})
	for _, mode := range []client.ScrollbarPosition{1, 2, 0} {
		s.onLoop(func() { s.ui.applyEvent(client.DisplayEvent{Type: "SCROLLBAR_UPDATE", Payload: mode}) })
		s.draw()
		s.onLoop(func() {
			for i, pane := range panes {
				if borderHasThumb(s, pane, false) != (mode == 1 && pane != s.ui.logsView) || borderHasThumb(s, pane, true) != (mode == 2 && pane != s.ui.logsView) {
					t.Errorf("pane %d did not place/hide its scrollbar for mode %d", i, mode)
				}
				x, y, w, h := pane.GetInnerRect()
				if diff := scrollbackDiff(before[i], scrollbackProjection(s.screen, x, y, w, h)); diff != "" {
					t.Errorf("scrollbar changed pane %d content: %s", i, diff)
				}
			}
		})
	}
}

func TestScrollbarUsesRetainedHistoryRatherThanEvictedRowsOrWindow(t *testing.T) {
	s := newSimTUI(t, 120, 32)
	s.onLoop(func() {
		s.ui.mu.Lock()
		s.ui.output = newLineBuffer(5, 2048)
		s.ui.outputSynced = -1
		s.ui.mu.Unlock()
		s.ui.applyEvent(client.DisplayEvent{Type: "SCROLLBAR_UPDATE", Payload: client.ScrollbarRight})
	})
	for i := range 30 {
		s.onLoop(func() { s.ui.appendOutput(fmt.Sprintf("retained %d", i)) })
		s.draw()
	}
	if !s.matches(func() bool { return s.ui.outputView.baseRow > 0 }) {
		t.Fatal("fixture did not evict previously measured rows")
	}
	if s.matches(func() bool { return borderHasThumb(s, s.ui.outputView, false) }) {
		t.Fatal("evicted rows created a scrollbar although all retained text fits")
	}
	s.onLoop(func() {
		s.ui.mu.Lock()
		s.ui.output = newLineBuffer(1000, 1<<20)
		s.ui.outputSynced = -1
		s.ui.mu.Unlock()
		for i := range 1000 {
			s.ui.appendOutput(fmt.Sprintf("long history %d", i))
		}
	})
	s.draw()
	thumbRows := onLoopValue(s, func() int {
		x, y, w, h := s.ui.outputView.GetRect()
		var count int
		for row := y + 1; row < y+h-1; row++ {
			if r, _, _, _ := s.screen.GetContent(x+w-1, row); r == '█' {
				count++
			}
		}
		return count
	})
	if thumbRows != 1 {
		t.Fatalf("thumb represents only the renderer window, not retained history: size %d", thumbRows)
	}
}
