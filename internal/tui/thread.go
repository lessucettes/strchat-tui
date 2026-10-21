package tui

import (
	"sort"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// threadView adds a gutter to the normal TextView without inserting characters
// into message text. Native wrapping, nickname styles and scrolling stay intact.
// All fields are owned by the event loop. The model bounds retained records.
type threadView struct {
	*tview.TextView
	enabled                   bool
	focus                     string
	accent, muted, background tcell.Color
	records                   []threadRecord
	spans                     []threadSpan
	indexed, width            int
}

type threadRecord struct {
	text, chat string
}

// One span per retained message, not per wrapped screen row. Very narrow
// terminals therefore cannot multiply the metadata budget by message length.
type threadSpan struct {
	chat       string
	start, end int
}

func newThreadView(view *tview.TextView) *threadView {
	v := &threadView{TextView: view}
	view.SetDrawFunc(func(_ tcell.Screen, x, y, w, h int) (int, int, int, int) {
		x, y, w, h = x+1, y+1, max(0, w-2), max(0, h-2)
		if v.enabled && w >= 3 {
			x, w = x+2, w-2
		}
		return x, y, w, h
	})
	return v
}

func (v *threadView) sync(delta lineDelta) {
	if delta.rewrite {
		v.records = nil
		v.spans = nil
		v.indexed = 0
	}
	for i, text := range delta.lines {
		chat := ""
		if i < len(delta.chats) {
			chat = delta.chats[i]
		}
		v.records = append(v.records, threadRecord{text: text, chat: chat})
	}
}

func (v *threadView) indexRows(width int) {
	if width != v.width {
		v.spans = nil
		v.indexed, v.width = 0, width
	}
	start := 1 // writeLines prefixes the retained buffer with a newline.
	if len(v.spans) > 0 {
		start = v.spans[len(v.spans)-1].end
	}
	for _, record := range v.records[v.indexed:] {
		end := start + len(tview.WordWrap(record.text, width))
		v.spans = append(v.spans, threadSpan{chat: record.chat, start: start, end: end})
		start = end
	}
	v.indexed = len(v.records)
}

func (v *threadView) Draw(screen tcell.Screen) {
	v.TextView.Draw(screen)
	_, _, outerWidth, _ := v.GetRect()
	if !v.enabled || outerWidth < 5 {
		return
	}
	x, y, width, height := v.GetInnerRect()
	if width <= 0 || height <= 0 {
		return
	}
	v.indexRows(width)
	offset, _ := v.GetScrollOffset()
	base := tcell.StyleDefault.Background(v.background)
	span := sort.Search(len(v.spans), func(i int) bool { return v.spans[i].end > offset })
	for row := 0; row < height && span < len(v.spans); row++ {
		position := offset + row
		for span < len(v.spans) && position >= v.spans[span].end {
			span++
		}
		if span == len(v.spans) {
			break
		}
		info := v.spans[span]
		if position < info.start {
			continue
		}
		first := position == info.start
		glyph, style := '│', base.Foreground(v.muted)
		selected := info.chat != "" && info.chat == v.focus
		if selected {
			style = base.Foreground(v.accent)
			if first {
				glyph = '●'
			}
		}
		screen.SetContent(x-2, y+row, glyph, nil, style)
		if selected && first {
			labelWidth := min(width, tview.TaggedStringWidth(sanitizeLineForDisplay(info.chat)))
			for column := 0; column < labelWidth; column++ {
				main, combining, original, _ := screen.GetContent(x+column, y+row)
				screen.SetContent(x+column, y+row, main, combining, original.Foreground(v.accent).Bold(true))
			}
		}
	}
}
