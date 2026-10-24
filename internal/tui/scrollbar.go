package tui

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/lessucettes/strchat-tui/internal/client"
)

// scrollbarPane decorates drawing only. Focus, input and widget identity stay
// with the existing primitive; the bar never changes the text rectangle.
type scrollbarPane struct {
	tview.Primitive
	ui *tui

	bar scrollbarGeometry

	// Info is small but wrapping still need not be recounted on every frame.
	infoKey   detailsPaneKey
	infoWidth int
	infoRows  int64
	infoValid bool
}

type scrollbarGeometry struct {
	x, y, height int
	thumb, size  int
	top, maximum int64
	page         int
}

func (t *tui) initScrollbars() {
	for i, primitive := range []tview.Primitive{t.outputView, t.logsView, t.chatList, t.detailsView} {
		t.scrollbars[i] = &scrollbarPane{Primitive: primitive, ui: t}
	}
}

func (t *tui) withScrollbar(primitive tview.Primitive) tview.Primitive {
	for _, pane := range t.scrollbars {
		if pane.Primitive == primitive {
			return pane
		}
	}
	return primitive
}

// Draw immediately after this widget, not after the entire root: an input
// completion popup must remain above both the underlying pane and its bar.
func (p *scrollbarPane) Draw(screen tcell.Screen) {
	p.Primitive.Draw(screen)
	p.bar = scrollbarGeometry{}
	if p.ui.scrollbarPosition == client.ScrollbarOff {
		return
	}
	x, y, width, height := p.GetRect()
	sw, sh := screen.Size()
	if width < 3 || height < 3 {
		return
	}
	if p.ui.scrollbarPosition == client.ScrollbarRight {
		x += width - 1
	}
	if x < 0 || x >= sw {
		return
	}
	start, end := max(0, y+1), min(sh, y+height-1)
	top, total, page := p.scrollRange()
	if p.Primitive == p.ui.logsView && page <= 1 {
		return // The compact Logs strip uses keyboard/wheel scrolling.
	}
	maximum := max(int64(0), total-int64(page))
	track := end - start
	if maximum == 0 || page <= 0 || track <= 0 {
		return
	}
	size := max(1, min(track-1, int(int64(track)*int64(page)/total)))
	top = min(max(top, 0), maximum)
	thumb := int(top * int64(track-size) / maximum)
	p.bar = scrollbarGeometry{x: x, y: start, height: track, thumb: thumb, size: size, top: top, maximum: maximum, page: page}
	style := tcell.StyleDefault.Foreground(p.ui.theme.borderColor).Background(p.ui.theme.backgroundColor)
	for row := range track {
		glyph, cellStyle := '│', style
		if row >= thumb && row < thumb+size {
			glyph, cellStyle = '█', style.Foreground(p.ui.theme.titleColor)
		}
		screen.SetContent(x, start+row, glyph, nil, cellStyle)
	}
}

func (p *scrollbarPane) scrollRange() (top, total int64, page int) {
	switch view := p.Primitive.(type) {
	case *scrollbackView:
		// Absolute row counters include evicted rows. The bar represents only
		// retained text, including its single leading blank row, not that gap
		// or the smaller TextView window used to render it.
		return max(0, view.top-view.baseRow), view.leading + view.sumRows, view.height
	case *tview.List:
		row, _ := view.GetOffset()
		_, _, _, page = view.GetInnerRect()
		return int64(row), int64(view.GetItemCount()), page
	case *tview.TextView:
		row, _ := view.GetScrollOffset()
		_, _, width, height := view.GetInnerRect()
		if !p.infoValid || p.infoKey != p.ui.detailsKey || p.infoWidth != width {
			p.infoRows = int64(view.GetWrappedLineCount())
			p.infoKey, p.infoWidth, p.infoValid = p.ui.detailsKey, width, true
		}
		return int64(row), p.infoRows, height
	}
	return 0, 0, 0
}

func (p *scrollbarPane) scrollTo(row int64) {
	row = min(max(row, 0), p.bar.maximum)
	switch view := p.Primitive.(type) {
	case *scrollbackView:
		if row == 0 {
			view.ScrollToBeginning()
		} else {
			view.ScrollTo(int(row+view.baseRow), view.column)
		}
	case *tview.List:
		// List.Draw always reveals its cursor. Keep that local preview cursor
		// visible without invoking SelectedFunc or activating any chat/group.
		current := view.GetCurrentItem()
		visible := min(max(current, int(row)), int(row)+p.bar.page-1)
		if current != visible {
			view.SetCurrentItem(visible)
		}
		_, column := view.GetOffset()
		view.SetOffset(int(row), column)
	case *tview.TextView:
		_, column := view.GetScrollOffset()
		view.ScrollTo(int(row), column)
	}
}

func (t *tui) restoreScrollbar(position client.ScrollbarPosition) {
	if position < client.ScrollbarOff || position > client.ScrollbarLeft {
		position = client.ScrollbarOff
	}
	if t.scrollbarPosition != position {
		t.scrollbarPosition = position
		t.scrollbarDrag = nil // still consume the held press's eventual release
	}
	if screen := t.screen.Load(); screen != nil {
		t.updateMouseReporting(*screen)
	}
}

func (t *tui) updateMouseReporting(screen tcell.Screen) {
	flags := tcell.MouseButtonEvents
	if t.scrollbarPosition != client.ScrollbarOff {
		flags = tcell.MouseDragEvents
	}
	if t.mouseScreen != screen || t.mouseFlags != flags {
		screen.EnableMouse(flags)
		t.mouseScreen, t.mouseFlags = screen, flags
	}
}

// Active presses capture moves/releases outside the original pane. Returning
// nil from application capture on release also suppresses tview's synthetic
// click, so neither a track press nor a drag can activate the row underneath.
func (t *tui) captureScrollbar(event *tcell.EventMouse, action tview.MouseAction) bool {
	if t.scrollbarPress {
		switch action {
		case tview.MouseMove:
			if event.Buttons()&tcell.Button1 == 0 {
				t.scrollbarPress, t.scrollbarDrag = false, nil
			} else if pane := t.scrollbarDrag; pane != nil {
				if !t.visible(pane.Primitive) || pane.bar.height == 0 || t.scrollbarPosition == client.ScrollbarOff {
					t.scrollbarDrag = nil
				} else {
					_, y := event.Position()
					travel := pane.bar.height - pane.bar.size
					if travel > 0 {
						position := min(max(y-pane.bar.y-t.scrollbarGrab, 0), travel)
						pane.scrollTo((int64(position)*pane.bar.maximum + int64(travel/2)) / int64(travel))
					} else {
						// A one-cell track in another narrow pane has no
						// travel to map onto; scroll by the drag distance.
						pane.scrollTo(t.scrollbarOrigin + int64(y-pane.bar.y-t.scrollbarGrab))
					}
				}
			}
			return true
		case tview.MouseLeftUp:
			t.scrollbarPress, t.scrollbarDrag = false, nil
			return true
		}
	}
	if action != tview.MouseLeftDown || t.scrollbarPosition == client.ScrollbarOff {
		return false
	}
	x, y := event.Position()
	for _, pane := range t.scrollbars {
		bar := pane.bar
		if !t.visible(pane.Primitive) || bar.height == 0 || x != bar.x || y < bar.y || y >= bar.y+bar.height {
			continue
		}
		t.scrollbarPress = true
		row := y - bar.y
		switch {
		case row < bar.thumb:
			pane.scrollTo(bar.top - int64(bar.page))
		case row >= bar.thumb+bar.size:
			pane.scrollTo(bar.top + int64(bar.page))
		default:
			t.scrollbarDrag, t.scrollbarGrab = pane, row-bar.thumb
			t.scrollbarOrigin = bar.top
		}
		return true
	}
	return false
}
