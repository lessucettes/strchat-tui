package tui

// scrollbackView is the bounded, incrementally maintained chat/log scrollback.
//
// It keeps the retained logical records (the model's canonical markup lines)
// plus one wrapped-row count per record at the current pane width, and gives
// tview a small window of whole records around the viewport. That keeps the
// cost of an appended message proportional to the new message and the visible
// window instead of the whole retained history, while leaving final line
// breaking, wide/combining-rune cells and style-tag parsing to tview — the
// renderer that already defines them.
//
// Row coordinates are absolute and never shift for a record that is still
// retained: evicting older records only advances baseRow. The viewport anchor
// is therefore stable while the user reads scrolled-away history, and bottom
// following is an explicit flag rather than a side effect of the widget text.
//
// All fields are owned by the tview event loop. The retained text itself is
// bounded by the model's line/byte caps; the extra window is bounded by
// maxScrollbackWindowBytes / a row cap proportional to the pane height, with
// the unavoidable exception of a single retained record.

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

const (
	// maxScrollbackWindowBytes bounds the styled text handed to tview. One
	// record may exceed it (client content is capped at 16 KiB, client-generated
	// payloads at 64 KiB); the window never holds more than such a record plus
	// its neighbours.
	maxScrollbackWindowBytes = 64 << 10

	// The window aims for this many extra viewport heights of slack so appends
	// and small scroll steps extend it instead of rebuilding it.
	scrollbackWindowSlack = 2
)

// scrollbackRecord is one retained display line and its group source label.
type scrollbackRecord struct {
	text string // canonical default-theme markup
	chat string // trusted group source label, "" outside group views
	rows int    // wrapped rows at rowsWidth; 0 means not measured yet
}

// scrollbackView implements tview.Primitive over its embedded TextView.
type scrollbackView struct {
	*tview.TextView

	gutter bool // reserve two columns for the group thread gutter
	border bool // border/padding is one cell, the same as tview's Box

	// Gutter appearance, refreshed by the render pass.
	enabled                   bool
	focus                     string
	accent, muted, background tcell.Color

	// Theme color mapper for canonical scrollback markup. nil means the
	// default theme, whose markup is already final.
	replacer *strings.Replacer

	// Retained records. head is the oldest live index; base is its absolute
	// logical line number and baseRow the number of wrapped rows before it at
	// rowsWidth. leading is the blank first row the previous implementation
	// always wrote before the retained text.
	records   []scrollbackRecord
	head      int
	base      int64
	baseRow   int64
	sumRows   int64
	leading   int64
	rowsWidth int
	rebase    bool

	// Viewport, in absolute wrapped rows (row 0 is the leading blank line).
	top      int64
	column   int
	follow   bool
	pageSize int
	width    int
	height   int

	// Window currently handed to the embedded TextView: whole records
	// [winStart, winEnd), whose first absolute row is winFirst.
	winStart   int
	winEnd     int
	winFirst   int64
	winRows    int64
	winBytes   int
	haveWindow bool
	needWindow bool
}

// newScrollbackView builds an empty pane. gutter enables the two-column group
// thread guide; the caller sets the presentation fields during render.
func newScrollbackView(gutter bool) *scrollbackView {
	v := &scrollbackView{
		TextView: tview.NewTextView(),
		gutter:   gutter,
		border:   true,
		follow:   true,
	}
	v.SetDynamicColors(true).SetScrollable(true)
	v.TextView.SetDrawFunc(func(_ tcell.Screen, x, y, width, height int) (int, int, int, int) {
		// Same border/gutter trim the embedded TextView must use.
		return v.innerRect(x, y, width, height)
	})
	return v
}

// SetBorder records the border inset used to predict the TextView content
// width before Draw. Both scrollback panes use a border.
func (v *scrollbackView) SetBorder(show bool) *tview.Box {
	v.border = show
	return v.TextView.SetBorder(show)
}

// innerRect maps an outer rectangle to the pane's text rectangle.
func (v *scrollbackView) innerRect(x, y, width, height int) (int, int, int, int) {
	if v.border {
		x, y = x+1, y+1
		width, height = width-2, height-2
	}
	width, height = max(0, width), max(0, height)
	if v.gutter && v.enabled && width >= 3 {
		x, width = x+2, width-2
	}
	return x, y, width, height
}

// count returns the number of retained records.
func (v *scrollbackView) count() int { return len(v.records) - v.head }

// rec returns a retained record by window-relative index.
func (v *scrollbackView) rec(i int) *scrollbackRecord { return &v.records[v.head+i] }

// totalRows is the absolute number of wrapped rows in the retained history,
// including the leading blank row.
func (v *scrollbackView) totalRows() int64 { return v.leading + v.baseRow + v.sumRows }

// firstRetainedRow is the absolute first row of the oldest retained record.
// Rows before it have been evicted and are no longer in the widget window.
func (v *scrollbackView) firstRetainedRow() int64 { return v.leading + v.baseRow }

// snapScrolledTop maps a scrolled viewport that eviction left in the evicted
// row range onto the oldest retained record. Row 0 remains the leading blank
// beginning-of-history row; any other position cannot be painted because the
// widget window starts at the oldest retained record.
func (v *scrollbackView) snapScrolledTop() {
	if v.count() == 0 {
		v.top = 0
		return
	}
	if first := v.firstRetainedRow(); v.top > 0 && v.top < first {
		v.top = first
	}
}

// recordStartRow returns the absolute first row of retained record i.
func (v *scrollbackView) recordStartRow(i int) int64 {
	row := v.firstRetainedRow()
	for j := 0; j < i; j++ {
		row += int64(v.rec(j).rows)
	}
	return row
}

// locate maps an absolute row to a retained record and the row offset inside
// it. ok is false when the row precedes the retained records.
func (v *scrollbackView) locate(abs int64) (index int, offset int64, ok bool) {
	if v.count() == 0 {
		return 0, 0, false
	}
	row := abs - v.leading - v.baseRow
	if row < 0 {
		return 0, 0, false
	}
	for i := 0; i < v.count(); i++ {
		rows := int64(v.rec(i).rows)
		if row < rows {
			return i, row, true
		}
		row -= rows
	}
	last := v.count() - 1
	return last, int64(max(1, v.rec(last).rows) - 1), true
}

// colored maps canonical default-theme markup to the active theme.
func (v *scrollbackView) colored(text string) string {
	if v.replacer == nil {
		return text
	}
	return v.replacer.Replace(text)
}

// wrappedRowCount reports how many screen rows a record occupies at width. It
// uses tview's own word-wrap algorithm so the row index and the renderer agree.
func wrappedRowCount(text string, width int) int {
	if width <= 0 {
		return 1
	}
	return len(tview.WordWrap(text, width))
}

// invalidateWindow forces the next draw to rebuild the widget window, for
// example after a theme change when canonical markup maps to new colors.
func (v *scrollbackView) invalidateWindow() { v.needWindow = true }

// sync folds a model delta into the retained records. It is incremental: an
// ordinary append copies only the new lines, an eviction drops only the
// obsolete records, and the widget window is touched only when it must change.
func (v *scrollbackView) sync(delta lineDelta) {
	if delta.first > v.base {
		v.evictTo(delta.first)
	} else if delta.first < v.base {
		// The model cannot un-evict lines; restart from what it now offers.
		v.reset(delta.from)
	}

	next := v.base + int64(v.count())
	from := max(delta.from, v.base)
	if from > next {
		// The view missed lines that were evicted before it could paint them.
		v.reset(delta.from)
		from, next = delta.from, delta.from
	}
	if from < next {
		// A theme/replay delta may repeat lines the view already holds.
		from = next
	}
	skip := int(from - delta.from)
	if skip > len(delta.lines) {
		skip = len(delta.lines)
	}
	if skip < len(delta.lines) {
		for i := skip; i < len(delta.lines); i++ {
			chat := ""
			if i < len(delta.chats) {
				chat = delta.chats[i]
			}
			v.records = append(v.records, scrollbackRecord{text: delta.lines[i], chat: chat})
		}
		if v.leading == 0 && v.rowsWidth > 0 {
			v.leading = 1
		}
	}
}

// reset restarts the retained records at an absolute model index.
func (v *scrollbackView) reset(start int64) {
	clear(v.records) // drop discarded strings before the backing array is reused
	v.records = v.records[:0]
	v.head = 0
	v.base = start
	v.baseRow = 0
	v.sumRows = 0
	v.leading = 0
	v.rowsWidth = 0
	v.rebase = false
	v.top = 0
	v.follow = true
	v.haveWindow = false
	v.needWindow = true
}

// evictTo drops records older than first and keeps the window valid when it
// does not contain any evicted record.
func (v *scrollbackView) evictTo(first int64) {
	dropped := 0
	windowStartAbs := v.base + int64(v.winStart)
	for v.count() > 0 && v.base < first {
		r := v.rec(0)
		if r.rows > 0 {
			v.baseRow += int64(r.rows)
			v.sumRows -= int64(r.rows)
		} else {
			// The dropped record was never measured: absolute row numbers can
			// no longer be derived, so rewrap and re-anchor at the next draw.
			v.rebase = true
		}
		*r = scrollbackRecord{}
		v.head++
		v.base++
		dropped++
	}
	if v.count() == 0 {
		v.records = v.records[:0]
		v.head = 0
		v.baseRow = 0
		v.sumRows = 0
		v.leading = 0
		v.rowsWidth = 0
	}
	if dropped == 0 {
		return
	}
	if v.haveWindow {
		if windowStartAbs >= first {
			v.winStart -= dropped
			v.winEnd -= dropped
		} else {
			// Evicted content was part of the widget window: drop it.
			v.invalidateWindow()
			v.winStart, v.winEnd = 0, 0
		}
	}
	v.compact()
	v.snapScrolledTop()
}

// compact moves retained records to the front once the head occupies at least
// half of the backing slice, so the backing array stays proportional to the
// bounded history rather than to all messages ever received.
func (v *scrollbackView) compact() {
	if v.head < 64 || v.head*2 < len(v.records) {
		return
	}
	n := copy(v.records, v.records[v.head:])
	clear(v.records[n:])
	v.records = v.records[:n]
	// winStart/winEnd index the retained records, not the backing array, so
	// moving those records to the front leaves the widget window unchanged.
	v.head = 0
}

// ensureRows measures wrapped-row counts for new records, or for all records
// when the pane width changed. A width change preserves the record and row
// under the viewport's top edge.
func (v *scrollbackView) ensureRows(width, height int) {
	if v.rebase {
		v.rowsWidth = 0
		v.baseRow = 0
		v.sumRows = 0
		v.rebase = false
		v.follow = true
	}
	if width <= 0 {
		return
	}
	if v.rowsWidth != width {
		anchorOK := false
		var anchorIndex int
		var anchorOffset int64
		if v.rowsWidth > 0 && v.count() > 0 && !v.follow {
			v.snapScrolledTop()
			anchorIndex, anchorOffset, anchorOK = v.locate(v.top)
		}
		v.rowsWidth = width
		v.baseRow = 0
		v.sumRows = 0
		for i := 0; i < v.count(); i++ {
			r := v.rec(i)
			r.rows = wrappedRowCount(r.text, width)
			v.sumRows += int64(r.rows)
		}
		if v.count() > 0 {
			v.leading = 1
		} else {
			v.leading = 0
		}
		switch {
		case v.follow:
			v.top = v.maxTop(height)
		case anchorOK:
			offset := min(anchorOffset, int64(max(1, v.rec(anchorIndex).rows)-1))
			v.top = v.recordStartRow(anchorIndex) + offset
		default:
			v.top = 0
		}
		v.invalidateWindow()
		return
	}
	for i := v.head; i < len(v.records); i++ {
		r := &v.records[i]
		if r.rows != 0 {
			continue
		}
		r.rows = wrappedRowCount(r.text, width)
		v.sumRows += int64(r.rows)
	}
	if v.count() > 0 {
		v.leading = 1
	}
}

// maxTop is the largest viewport top row for the pane height. When the whole
// retained tail fits in the pane, the evicted rows before it are not part of
// the widget text, so the viewport sits at the beginning instead of an
// unpaintable absolute row inside the evicted gap.
func (v *scrollbackView) maxTop(height int) int64 {
	if v.count() > 0 && v.leading+v.sumRows <= int64(height) {
		return 0
	}
	return max(0, v.totalRows()-int64(height))
}

// ensureWindow makes the embedded TextView hold a bounded window of records
// covering the viewport, then positions it at the same absolute row.
func (v *scrollbackView) ensureWindow(width, height int) {
	v.width, v.height, v.pageSize = width, height, height
	v.ensureRows(width, height)
	maxTop := v.maxTop(height)
	if v.follow {
		v.top = maxTop
	} else {
		v.top = min(max(v.top, 0), maxTop)
		if v.top == maxTop {
			v.follow = true
		}
	}
	v.snapScrolledTop()
	if v.top >= maxTop {
		v.follow = true
	}

	if v.count() == 0 {
		if v.haveWindow || v.needWindow {
			v.TextView.SetText("")
			v.haveWindow, v.needWindow = false, false
		}
		return
	}

	if !v.haveWindow || v.needWindow {
		v.rebuildWindow(height)
	} else if v.top < v.winFirst {
		// The user scrolled above the cached window.
		v.rebuildWindow(height)
	} else if v.top+int64(height) > v.winFirst+v.winRows {
		// Following the end means the window bottom equals the old content
		// bottom; newly appended records can cover the difference cheaply.
		if v.winEnd < v.count() {
			v.extendWindow(height, v.top+int64(height)+int64(height/2))
		}
		if v.top+int64(height) > v.winFirst+v.winRows {
			v.rebuildWindow(height)
		}
	} else if v.winEnd < v.count() {
		target := v.top + int64(height) + int64(height/2)
		if v.winFirst+v.winRows < target {
			v.extendWindow(height, target)
		}
	}
	v.TextView.ScrollTo(int(v.top-v.winFirst), v.column)
}

// rebuildWindow replaces the widget text with a bounded run of whole records
// around the viewport.
func (v *scrollbackView) rebuildWindow(height int) {
	v.haveWindow, v.needWindow = false, false
	count := v.count()
	if count == 0 {
		v.TextView.SetText("")
		return
	}

	top := v.top
	includeLeading := top < v.leading
	start := 0
	firstRow := int64(0)
	windowBytes := 0
	countedThrough := -1 // last record already included in windowBytes
	if includeLeading {
		firstRow = 0
	} else {
		index, _, ok := v.locate(top)
		if !ok {
			index = 0
		}
		start = index
		firstRow = v.recordStartRow(start)
		windowBytes = len(v.rec(start).text)

		// Extend upward for half a viewport of scroll slack, bounded.
		half := int64(height / 2)
		for start > 0 {
			prev := start - 1
			prevRows := int64(max(1, v.rec(prev).rows))
			if firstRow-prevRows < top-half {
				break
			}
			if windowBytes+len(v.rec(prev).text) > maxScrollbackWindowBytes {
				break
			}
			start = prev
			firstRow -= prevRows
			windowBytes += len(v.rec(prev).text)
		}
		countedThrough = index
	}

	// Extend downward until the viewport and its slack are covered, keeping
	// the window bounded. A single record larger than the cap is unavoidable.
	rows := 0
	bytes := windowBytes
	coverBottom := top + int64(height)
	extendBottom := coverBottom + int64(height/2)
	capRows := int64(height) + scrollbackWindowSlack*int64(height)
	end := start
	row := firstRow
	for end < count {
		r := v.rec(end)
		rowAfter := row + int64(max(1, r.rows))
		if end > start && row >= coverBottom &&
			(rowAfter > extendBottom || int64(rows+r.rows) > capRows || bytes+len(r.text) > maxScrollbackWindowBytes) {
			break
		}
		rows += max(1, r.rows)
		if end > countedThrough {
			bytes += len(r.text)
		}
		row = rowAfter
		end++
		if row >= extendBottom {
			break
		}
	}

	text := v.windowText(start, end, includeLeading)
	v.TextView.SetText(text)
	v.winStart, v.winEnd = start, end
	v.winFirst = firstRow
	v.winRows = int64(rows)
	if includeLeading {
		v.winRows++
	}
	v.winBytes = bytes
	v.haveWindow = true
}

// windowText joins whole records for the widget, optionally keeping the
// leading blank row the previous writer always emitted.
func (v *scrollbackView) windowText(start, end int, leading bool) string {
	if end <= start {
		return ""
	}
	size := 0
	for i := start; i < end; i++ {
		size += len(v.rec(i).text) + 1
	}
	var b strings.Builder
	b.Grow(size)
	if leading {
		b.WriteByte('\n')
	}
	for i := start; i < end; i++ {
		if i > start {
			b.WriteByte('\n')
		}
		b.WriteString(v.colored(v.rec(i).text))
	}
	return b.String()
}

// extendWindow appends following records to the current widget window until it
// reaches targetRow. This keeps a followed append from rebuilding the window
// on every message; the window is rebuilt only when it outgrows its bound.
func (v *scrollbackView) extendWindow(height int, targetRow int64) {
	windowBottom := v.winFirst + v.winRows
	if windowBottom >= targetRow {
		return
	}
	var b strings.Builder
	rows, bytes := v.winRows, v.winBytes
	for v.winEnd < v.count() && windowBottom < targetRow && bytes < maxScrollbackWindowBytes {
		r := v.rec(v.winEnd)
		if rows > int64(height) && bytes+len(r.text) > maxScrollbackWindowBytes {
			break
		}
		b.WriteByte('\n')
		b.WriteString(v.colored(r.text))
		rows += int64(max(1, r.rows))
		bytes += len(r.text)
		windowBottom += int64(max(1, r.rows))
		v.winEnd++
	}
	if b.Len() > 0 {
		fmt.Fprint(v.TextView, b.String())
		v.winRows, v.winBytes = rows, bytes
	}
	if v.winRows > int64(height)+scrollbackWindowSlack*int64(height) {
		v.rebuildWindow(height)
	}
}

// Draw paints the pane: it first makes the widget window match the viewport,
// then draws that window with tview's own wrapping/style parsing, then paints
// the group gutter over it.
func (v *scrollbackView) Draw(screen tcell.Screen) {
	outerX, outerY, outerWidth, outerHeight := v.GetRect()
	contentX, contentY, contentWidth, contentHeight := v.innerRect(outerX, outerY, outerWidth, outerHeight)
	if contentWidth <= 0 || contentHeight <= 0 {
		v.TextView.Draw(screen)
		return
	}
	v.ensureWindow(contentWidth, contentHeight)
	v.TextView.Draw(screen)
	v.drawGutter(screen, contentX, contentY, contentWidth, contentHeight)
}

// drawGutter paints the narrow thread guide and highlights the focused
// conversation's first row. It shares the row index used for scrolling.
func (v *scrollbackView) drawGutter(screen tcell.Screen, x, y, width, height int) {
	if !v.gutter || !v.enabled || width <= 0 || v.count() == 0 {
		return
	}
	index, offset := 0, int64(0)
	startRow := 0
	if v.top < v.leading {
		// The leading blank row occupies the first screen row.
		startRow = int(v.leading - v.top)
	} else {
		var ok bool
		index, offset, ok = v.locate(v.top)
		if !ok {
			return
		}
	}
	base := tcell.StyleDefault.Background(v.background)
	for row := startRow; row < height && index < v.count(); row++ {
		r := v.rec(index)
		selected := r.chat != "" && r.chat == v.focus
		first := offset == 0
		glyph, style := '│', base.Foreground(v.muted)
		if selected {
			style = base.Foreground(v.accent)
			if first {
				glyph = '●'
			}
		}
		screen.SetContent(x-2, y+row, glyph, nil, style)
		if selected && first {
			labelWidth := min(width, tview.TaggedStringWidth(sanitizeLineForDisplay(r.chat)))
			for column := 0; column < labelWidth; column++ {
				main, combining, original, _ := screen.GetContent(x+column, y+row)
				screen.SetContent(x+column, y+row, main, combining, original.Foreground(v.accent).Bold(true))
			}
		}
		offset++
		if offset >= int64(max(1, r.rows)) {
			index, offset = index+1, 0
		}
	}
}

// ScrollTo moves the viewport to an absolute retained row (0 is the oldest
// retained row, including the leading blank line) and column.
func (v *scrollbackView) ScrollTo(row, column int) *scrollbackView {
	if v.width > 0 {
		v.ensureRows(v.width, v.height)
	}
	v.column = column
	v.top = int64(max(0, row))
	maxTop := v.maxTop(v.height)
	if v.top > maxTop {
		v.top = maxTop
	}
	v.snapScrolledTop()
	v.follow = v.top >= maxTop
	return v
}

// ScrollToBeginning shows the oldest retained row and stops following.
func (v *scrollbackView) ScrollToBeginning() *scrollbackView {
	v.follow = false
	v.top = 0
	return v
}

// ScrollToEnd follows the newest row.
func (v *scrollbackView) ScrollToEnd() *scrollbackView {
	v.follow = true
	v.top = v.maxTop(v.height)
	v.snapScrolledTop()
	return v
}

// GetScrollOffset returns the absolute retained viewport row and column.
func (v *scrollbackView) GetScrollOffset() (row, column int) {
	return int(v.top), v.column
}

// scrollRows moves the viewport by delta rows without leaving the retained
// history, returning true when it is now following the end.
func (v *scrollbackView) scrollRows(delta int) bool {
	if v.width > 0 {
		v.ensureRows(v.width, v.height)
	}
	top := min(max(v.top+int64(delta), 0), v.maxTop(v.height))
	if v.count() > 0 {
		if first := v.firstRetainedRow(); top > 0 && top < first {
			// Upward movement past the oldest record returns to the leading
			// blank beginning row; downward movement lands on the first
			// retained record. Never leave the viewport in the evicted gap.
			if delta < 0 {
				top = 0
			} else {
				top = first
			}
		}
	}
	v.top = top
	v.follow = v.top >= v.maxTop(v.height)
	return v.follow
}

// scrollPage pages by one viewport height, preserving tview's track-end
// behavior: paging down to the end resumes following, any upward move stops.
func (v *scrollbackView) scrollPage(delta int) {
	page := max(1, v.pageSize)
	if delta < 0 {
		v.follow = false
	}
	v.scrollRows(delta * page)
}

// InputHandler provides the same scrolling keys as tview.TextView.
func (v *scrollbackView) InputHandler() func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
	return v.WrapInputHandler(func(event *tcell.EventKey, _ func(p tview.Primitive)) {
		switch event.Key() {
		case tcell.KeyRune:
			switch event.Rune() {
			case 'g':
				v.follow = false
				v.top = 0
				v.column = 0
			case 'G':
				v.ScrollToEnd()
				v.column = 0
			case 'j':
				v.scrollRows(1)
			case 'k':
				v.follow = false
				v.scrollRows(-1)
			case 'h':
				v.column--
			case 'l':
				v.column++
			}
		case tcell.KeyHome:
			v.follow = false
			v.top = 0
			v.column = 0
		case tcell.KeyEnd:
			v.ScrollToEnd()
			v.column = 0
		case tcell.KeyUp:
			v.follow = false
			v.scrollRows(-1)
		case tcell.KeyDown:
			v.scrollRows(1)
		case tcell.KeyLeft:
			v.column--
		case tcell.KeyRight:
			v.column++
		case tcell.KeyPgDn, tcell.KeyCtrlF:
			v.scrollPage(1)
		case tcell.KeyPgUp, tcell.KeyCtrlB:
			v.scrollPage(-1)
		}
	})
}

// MouseHandler scrolls with the wheel and takes focus on a left press, like
// tview.TextView.
func (v *scrollbackView) MouseHandler() func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(p tview.Primitive)) (bool, tview.Primitive) {
	return v.WrapMouseHandler(func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(p tview.Primitive)) (bool, tview.Primitive) {
		if !v.InRect(event.Position()) {
			return false, nil
		}
		switch action {
		case tview.MouseLeftDown:
			setFocus(v)
			return true, nil
		case tview.MouseLeftClick:
			return true, nil
		case tview.MouseScrollUp:
			v.follow = false
			v.scrollRows(-1)
			return true, nil
		case tview.MouseScrollDown:
			v.scrollRows(1)
			return true, nil
		}
		return false, nil
	})
}

// GetText returns the retained scrollback as text. strip removes our style
// tags; GetText is a diagnostic/test accessor, never used on the render path.
func (v *scrollbackView) GetText(strip bool) string {
	if v.count() == 0 {
		return ""
	}
	size := 1
	for i := 0; i < v.count(); i++ {
		size += len(v.rec(i).text) + 1
	}
	var b strings.Builder
	b.Grow(size)
	b.WriteByte('\n')
	for i := 0; i < v.count(); i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(v.colored(v.rec(i).text))
	}
	text := b.String()
	if !strip {
		return text
	}
	// Reuse tview's own tag parser so stripping matches what it draws, even
	// for escaped user text such as "[red[]".
	scratch := tview.NewTextView().SetDynamicColors(true)
	scratch.SetText(text)
	return scratch.GetText(true)
}

// GetOriginalLineCount returns the number of logical lines in the retained
// text (the leading blank line plus embedded newlines), without wrapping.
func (v *scrollbackView) GetOriginalLineCount() int {
	if v.count() == 0 {
		return 0
	}
	lines := 1
	for i := 0; i < v.count(); i++ {
		lines += 1 + strings.Count(v.rec(i).text, "\n")
	}
	return lines
}

// GetWrappedLineCount returns the number of wrapped rows over the whole
// retained history at the current pane width.
func (v *scrollbackView) GetWrappedLineCount() int {
	if v.width > 0 {
		v.ensureRows(v.width, v.height)
	}
	return int(v.totalRows())
}
