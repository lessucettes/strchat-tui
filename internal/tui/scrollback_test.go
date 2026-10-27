package tui

// Correctness tests for the windowed scrollback. The reference is a plain
// tview.TextView holding the whole retained text: every visible cell (text,
// combining runes and style) must match it at the same absolute row offset.

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/lessucettes/strchat-tui/internal/client"
)

// scrollbackCell is one compared screen cell.
type scrollbackCell struct {
	r         rune
	combining []rune
	style     tcell.Style
}

func scrollbackProjection(screen tcell.SimulationScreen, x, y, width, height int) [][]scrollbackCell {
	cells, _, _ := screen.GetContents()
	sw, _ := screen.Size()
	out := make([][]scrollbackCell, height)
	for row := 0; row < height; row++ {
		out[row] = make([]scrollbackCell, width)
		for col := 0; col < width; col++ {
			c := cells[(y+row)*sw+x+col]
			cell := scrollbackCell{r: ' ', style: c.Style}
			if len(c.Runes) > 0 {
				cell.r = c.Runes[0]
				cell.combining = append([]rune(nil), c.Runes[1:]...)
			}
			out[row][col] = cell
		}
	}
	return out
}

func scrollbackDiff(want, got [][]scrollbackCell) string {
	for row := range want {
		for col := range want[row] {
			w, g := want[row][col], got[row][col]
			if w.r != g.r || string(w.combining) != string(g.combining) || w.style != g.style {
				return fmt.Sprintf("row %d col %d: want %q/%v/%v got %q/%v/%v",
					row, col, w.r, w.combining, w.style, g.r, g.combining, g.style)
			}
		}
	}
	return ""
}

// scrollbackCorpus contains awkward content: wide runes, an emoji ZWJ
// sequence, combining marks, explicit newlines, our own style tags and escaped
// user markup that must stay literal.
func scrollbackCorpus() []string {
	return []string{
		"[#32cd32]coffee[-] [#33ccff]alice[-::-]#abcd> wide 世界 👩\u200d💻 [red[] literal [#808080][id0 12:00:00][-]",
		"[#32cd32]coffee[-] [#ff00ff]bob[-::-]#4321> first line\nsecond line with e\u0301 and 👨\u200d👩\u200d👧\u200d👦 [#808080][id1 12:00:00][-]",
		"[#808080]-- a long unbroken token " + strings.Repeat("x", 90) + "[-]",
		"[#32cd32]coffee[-] [#ffff00]carol[-::-]#beef> " + strings.Repeat("word ", 30) + "[#808080][id2 12:00:00][-]",
		"",
		"[#808080]-- trailing newline\n[-]",
		"[#32cd32]coffee[-] [#33ccff]dave[-::-]#0000> tab-free CRLF sanitized\r\nnext line [#808080][id3 12:00:00][-]",
	}
}

// feedScrollback appends records in model order through the production delta.
func feedScrollback(v *scrollbackView, lines, chats []string, first int64) {
	v.sync(lineDelta{lines: lines, chats: chats, from: first, upto: first + int64(len(lines)) - 1, first: first})
}

func TestScrollbackPagingAliases(t *testing.T) {
	for _, logs := range []bool{false, true} {
		for _, maximized := range []bool{false, true} {
			t.Run(fmt.Sprintf("logs=%t/maximized=%t", logs, maximized), func(t *testing.T) {
				s := newSimTUI(t, 120, 32)
				pane := s.ui.outputView
				mode := maximizeMessages
				if logs {
					pane, mode = s.ui.logsView, maximizeLogs
				}
				s.onLoop(func() {
					for i := range 100 {
						s.ui.appendOutput(fmt.Sprintf("message %d", i))
						s.ui.appendLogLine(fmt.Sprintf("log %d", i))
					}
					s.ui.input.SetText("draft")
					s.ui.setFocus(pane)
					if maximized {
						s.ui.setMaximized(mode)
					}
				})
				s.draw()
				s.key(tcell.KeyPgUp, 0, tcell.ModNone)
				waitFor(t, time.Second, func() bool { return s.matches(func() bool { return !pane.follow }) })
				up := onLoopValue(s, func() int64 { return pane.top })
				s.key(tcell.KeyPgDn, 0, tcell.ModNone)
				waitFor(t, time.Second, func() bool { return s.matches(func() bool { return pane.follow }) })
				down := onLoopValue(s, func() int64 { return pane.top })
				if up >= down {
					t.Fatal("fixture did not scroll by a page")
				}
				s.key(tcell.KeyRune, 'v', tcell.ModAlt)
				waitFor(t, time.Second, func() bool {
					return s.matches(func() bool { return pane.top == up && !pane.follow })
				})
				s.key(tcell.KeyCtrlV, 0, tcell.ModCtrl)
				waitFor(t, time.Second, func() bool {
					return s.matches(func() bool { return pane.top == down && pane.follow })
				})
				s.key(tcell.KeyUp, 0, tcell.ModNone)
				waitFor(t, time.Second, func() bool { return s.matches(func() bool { return !pane.follow }) })
				previous := onLoopValue(s, func() int64 { return pane.top })
				s.key(tcell.KeyDown, 0, tcell.ModNone)
				waitFor(t, time.Second, func() bool { return s.matches(func() bool { return pane.follow }) })
				s.key(tcell.KeyCtrlP, 0, tcell.ModCtrl)
				waitFor(t, time.Second, func() bool {
					return s.matches(func() bool { return pane.top == previous && !pane.follow })
				})
				s.key(tcell.KeyCtrlN, 0, tcell.ModCtrl)
				waitFor(t, time.Second, func() bool {
					return s.matches(func() bool { return pane.top == down && pane.follow })
				})
				if !s.matches(func() bool { return s.ui.focused == pane && s.ui.input.GetText() == "draft" }) {
					t.Fatal("paging changed focus or the input draft")
				}
			})
		}
	}
}

func TestScrollbackMatchesReferenceTextView(t *testing.T) {
	lines := scrollbackCorpus()
	chats := make([]string, len(lines))
	for i := range chats {
		chats[i] = "coffee"
	}
	for _, size := range [][2]int{{14, 8}, {18, 12}, {40, 16}, {80, 24}, {120, 40}} {
		for _, fraction := range []int{0, 30, 100} {
			t.Run(fmt.Sprintf("%dx%d/%d", size[0], size[1], fraction), func(t *testing.T) {
				v := newScrollbackView(false)
				v.SetBorder(true)
				v.enabled = false
				feedScrollback(v, lines, chats, 0)

				own := tcell.NewSimulationScreen("UTF-8")
				if err := own.Init(); err != nil {
					t.Fatal(err)
				}
				defer own.Fini()
				own.SetSize(size[0], size[1])
				v.SetRect(0, 0, size[0], size[1])
				v.Draw(own)

				// Scroll to an absolute row that the reference also understands.
				maxTop := max(0, v.totalRows()-int64(size[1]-2))
				v.top = maxTop * int64(fraction) / 100
				v.follow = v.top == maxTop
				v.Draw(own)

				reference := tview.NewTextView().SetDynamicColors(true)
				reference.SetBorder(true)
				reference.SetText(v.GetText(false))
				reference.SetRect(0, 0, size[0], size[1])
				reference.ScrollTo(int(v.top), 0)
				ref := tcell.NewSimulationScreen("UTF-8")
				if err := ref.Init(); err != nil {
					t.Fatal(err)
				}
				defer ref.Fini()
				ref.SetSize(size[0], size[1])
				reference.Draw(ref)

				x, y, width, height := v.GetInnerRect()
				want := scrollbackProjection(ref, x, y, width, height)
				got := scrollbackProjection(own, x, y, width, height)
				if diff := scrollbackDiff(want, got); diff != "" {
					t.Fatalf("windowed scrollback differs from full TextView at top=%d: %s", v.top, diff)
				}
			})
		}
	}
}

func TestScrollbackKeepsHelpHeadingVisibleAcrossRecords(t *testing.T) {
	s := newSimTUI(t, 58, 10)
	s.onLoop(func() {
		s.ui.applyEvent(client.DisplayEvent{Type: "INFO", Content: "No chats joined. Use /join <chat> and /help. Public messages are not encrypted."})
		s.ui.applyEvent(client.DisplayEvent{Type: "INFO", Content: "CATEGORY:\n" + strings.Repeat("  Command description.\n", 40)})
		s.ui.render()
		s.ui.outputView.ScrollToBeginning()
	})
	s.draw()
	if !strings.Contains(s.text(), "CATEGORY:") {
		t.Fatalf("oversized help record was omitted despite starting in the viewport:\n%s", s.text())
	}
}

// TestScrollbackAnchorsSurviveEviction verifies the core invariant: evicting
// older records must not move the content a scrolled-away reader is looking at,
// while a bottom-following reader keeps seeing the newest record.
func TestScrollbackAnchorsSurviveEviction(t *testing.T) {
	v := newScrollbackView(false)
	v.SetBorder(true)
	own := tcell.NewSimulationScreen("UTF-8")
	if err := own.Init(); err != nil {
		t.Fatal(err)
	}
	defer own.Fini()
	own.SetSize(60, 20)
	v.SetRect(0, 0, 60, 20)

	var lines, chats []string
	for i := range 120 {
		lines = append(lines, fmt.Sprintf("[#808080]line %03d %s[-]", i, strings.Repeat("padding ", 3)))
		chats = append(chats, "")
	}
	feedScrollback(v, lines, chats, 0)
	v.Draw(own)

	// Park the viewport in the middle of the readable history.
	maxTop := v.maxTop(v.height)
	v.top = maxTop / 2
	v.follow = false
	v.invalidateWindow()
	v.Draw(own)
	before := scrollbackProjection(own, 1, 1, v.width, v.height)
	beforeTop := v.top

	// Push new records while the model evicts records older than index 40; the
	// anchored records themselves survive and must not move.
	for batch := range 3 {
		var more []string
		for i := range 100 {
			more = append(more, fmt.Sprintf("[#808080]new %d-%03d %s[-]", batch, i, strings.Repeat("padding ", 3)))
		}
		first := v.base + int64(v.count())
		v.sync(lineDelta{lines: more, chats: make([]string, len(more)), from: first, upto: first + 99, first: 40})
		v.Draw(own)
	}
	if v.top != beforeTop {
		t.Fatalf("anchor moved: top %d -> %d", beforeTop, v.top)
	}
	after := scrollbackProjection(own, 1, 1, v.width, v.height)
	if diff := scrollbackDiff(before, after); diff != "" {
		t.Fatalf("anchored viewport changed after eviction: %s", diff)
	}

	// Returning to the end follows the newest record again.
	v.ScrollToEnd()
	v.invalidateWindow()
	v.Draw(own)
	if !v.follow || !strings.Contains(v.GetText(false), "new 2-099") {
		t.Fatalf("end following lost the newest record (follow=%v)", v.follow)
	}
}

// TestScrollbackWindowStaysBounded checks the explicit window cap at a very
// narrow width where one message can wrap into many rows.
func TestScrollbackWindowStaysBounded(t *testing.T) {
	v := newScrollbackView(true)
	v.SetBorder(true)
	v.enabled = true
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(16, 10)
	v.SetRect(0, 0, 16, 10)

	var lines, chats []string
	for i := range maxOutputLines {
		lines = append(lines, fmt.Sprintf("[#808080]m%04d %s[-]", i, strings.Repeat("wide 世界 ", 30)))
		chats = append(chats, "coffee")
	}
	for from := 0; from < len(lines); from += 100 {
		to := min(from+100, len(lines))
		v.sync(lineDelta{lines: lines[from:to], chats: chats[from:to], from: int64(from), upto: int64(to - 1), first: 0})
		v.evictTo(max(0, int64(to)-1000))
		v.Draw(screen)
		if v.winBytes > maxScrollbackWindowBytes+maxLineRunes*8 {
			t.Fatalf("window bytes %d exceed bound", v.winBytes)
		}
		capRows := int64(v.height) + scrollbackWindowSlack*int64(v.height)
		if v.winRows > capRows+4000 { // one very long record may exceed the row cap
			t.Fatalf("window rows %d exceed bound %d", v.winRows, capRows)
		}
	}
	if v.count() > maxOutputLines {
		t.Fatalf("retained records %d exceed the model bound", v.count())
	}
	if text := v.GetText(false); len(text) > maxOutputBytes+maxOutputLines+100 {
		t.Fatalf("retained text %d exceeds the byte bound", len(text))
	}
}

// TestScrollbackRandomViewportMatchesReference is a deterministic state-machine
// check over appends, evictions, navigation, scrolling and resizes. Every draw
// must match a full TextView and must never leave the viewport in evicted rows.
func TestScrollbackRandomViewportMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	v := newScrollbackView(false)
	v.SetBorder(true)
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	outerW, outerH := 80, 30
	screen.SetSize(outerW, outerH)
	v.SetRect(0, 0, outerW, outerH)
	feedScrollback(v, scrollbackNavLines("seed", 40), make([]string, 40), 0)
	modelFirst := int64(0)

	for step := 0; step < 400; step++ {
		switch rng.Intn(12) {
		case 0, 1:
			n := 1 + rng.Intn(8)
			if n > 0 {
				next := v.base + int64(v.count())
				first := modelFirst + int64(rng.Intn(n+1))
				if first > next {
					first = next
				}
				if first < modelFirst {
					first = modelFirst
				}
				lines := make([]string, n)
				chats := make([]string, n)
				for i := range lines {
					lines[i] = fmt.Sprintf("[#808080]rnd %04d %s[-]", step*10+i, strings.Repeat("word ", rng.Intn(18)))
					if rng.Intn(2) == 0 {
						chats[i] = "coffee"
					}
				}
				v.sync(lineDelta{lines: lines, chats: chats, from: next, upto: next + int64(n) - 1, first: first})
				modelFirst = first
			}
		case 2:
			pressScrollbackKey(v, tcell.KeyHome)
		case 3:
			pressScrollbackKey(v, tcell.KeyEnd)
		case 4:
			pressScrollbackKey(v, tcell.KeyUp)
		case 5:
			pressScrollbackKey(v, tcell.KeyDown)
		case 6:
			pressScrollbackKey(v, tcell.KeyPgUp)
		case 7:
			pressScrollbackKey(v, tcell.KeyPgDn)
		case 8:
			v.ScrollTo(rng.Intn(int(v.totalRows())+1), 0)
		case 9, 10:
			outerW = []int{40, 58, 80, 113}[rng.Intn(4)]
			outerH = []int{12, 24, 36}[rng.Intn(3)]
			screen.SetSize(outerW, outerH)
			v.SetRect(0, 0, outerW, outerH)
		case 11:
			// A skipped draw followed by another eviction exercises the
			// "cursor fell behind eviction" reset path.
			next := v.base + int64(v.count())
			first := modelFirst + int64(rng.Intn(20))
			if first > next {
				first = next
			}
			lines := []string{fmt.Sprintf("[#808080]gap %04d[-]", step)}
			v.sync(lineDelta{lines: lines, chats: []string{""}, from: next, upto: next, first: first})
			modelFirst = first
		}
		v.Draw(screen)
		assertScrollbackViewportSize(t, v, screen, outerW, outerH)
	}
}

// scrollbackNavLines builds one-row records used by the viewport regressions.
func scrollbackNavLines(prefix string, n int) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("[#808080]%s %04d[-]", prefix, i)
	}
	return lines
}

// newScrollbackNavView builds the simulated 60x24 pane shared by navigation
// regressions. Content comparisons use the ordinary pane where the reference
// TextView has the same text width.
func newScrollbackNavView(t *testing.T) (*scrollbackView, tcell.SimulationScreen) {
	t.Helper()
	v := newScrollbackView(false)
	v.SetBorder(true)
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(screen.Fini)
	screen.SetSize(60, 24)
	v.SetRect(0, 0, 60, 24)
	return v, screen
}

// pressScrollbackKey drives the production key handler.
func pressScrollbackKey(v *scrollbackView, key tcell.Key) {
	v.InputHandler()(tcell.NewEventKey(key, 0, tcell.ModNone), nil)
}

// feedAndEvict appends new records while the model drops everything older
// than first, matching lineDeltaLocked's contract.
func feedAndEvict(v *scrollbackView, lines []string, first int64) {
	from := v.base + int64(v.count())
	v.sync(lineDelta{
		lines: lines,
		chats: make([]string, len(lines)),
		from:  from,
		upto:  from + int64(len(lines)) - 1,
		first: first,
	})
}

// assertScrollbackViewport compares every visible cell with a full TextView
// holding the retained text. top==0 is the leading blank row; after eviction the
// remaining viewport rows address retained text at top-baseRow.
func assertScrollbackViewport(t *testing.T, v *scrollbackView, screen tcell.SimulationScreen) {
	t.Helper()
	assertScrollbackViewportSize(t, v, screen, 60, 24)
}

func assertScrollbackViewportSize(t *testing.T, v *scrollbackView, screen tcell.SimulationScreen, outerW, outerH int) {
	t.Helper()
	if v.count() == 0 {
		return
	}
	firstRetained := v.leading + v.baseRow
	if v.top > 0 && v.top < firstRetained {
		t.Fatalf("viewport top %d lies in evicted rows [1,%d)", v.top, firstRetained)
	}
	offset := 0
	if v.top > 0 {
		offset = int(v.top - v.baseRow)
	}
	ref := tview.NewTextView().SetDynamicColors(true)
	ref.SetBorder(true)
	ref.SetText(v.GetText(false))
	ref.SetRect(0, 0, outerW, outerH)
	ref.ScrollTo(offset, 0)
	refScreen := tcell.NewSimulationScreen("UTF-8")
	if err := refScreen.Init(); err != nil {
		t.Fatal(err)
	}
	defer refScreen.Fini()
	refScreen.SetSize(outerW, outerH)
	ref.Draw(refScreen)

	x, y, width, height := v.innerRect(0, 0, outerW, outerH)
	want := scrollbackProjection(refScreen, x, y, width, height)
	got := scrollbackProjection(screen, x, y, width, height)
	if diff := scrollbackDiff(want, got); diff != "" {
		t.Fatalf("viewport top=%d (reference %d) differs: %s", v.top, offset, diff)
	}
}

// TestScrollbackSaturatedEvictionNavigation pins the beginning-of-retained
// behavior after the model has evicted more rows than a viewport: Home, Up and
// PageUp reach the oldest retained row without parking the viewport in the
// evicted gap, and paging down lands on the first retained record.
func TestScrollbackSaturatedEvictionNavigation(t *testing.T) {
	v, screen := newScrollbackNavView(t)
	feedScrollback(v, scrollbackNavLines("line", 300), make([]string, 300), 0)
	v.Draw(screen)
	feedAndEvict(v, scrollbackNavLines("new", 300), 200)
	v.Draw(screen)
	if v.baseRow == 0 {
		t.Fatal("fixture did not evict wrapped rows")
	}
	first := v.leading + v.baseRow

	pressScrollbackKey(v, tcell.KeyHome)
	v.Draw(screen)
	if v.top != 0 {
		t.Fatalf("Home top = %d, want 0", v.top)
	}
	assertScrollbackViewport(t, v, screen)

	pressScrollbackKey(v, tcell.KeyUp)
	v.Draw(screen)
	if v.top != 0 {
		t.Fatalf("Up at Home top = %d, want 0", v.top)
	}
	assertScrollbackViewport(t, v, screen)

	pressScrollbackKey(v, tcell.KeyPgUp)
	v.Draw(screen)
	if v.top != 0 {
		t.Fatalf("PageUp at Home top = %d, want 0", v.top)
	}
	assertScrollbackViewport(t, v, screen)

	pressScrollbackKey(v, tcell.KeyPgDn)
	v.Draw(screen)
	if v.top != first {
		t.Fatalf("PageDown from Home top = %d, want first retained row %d", v.top, first)
	}
	assertScrollbackViewport(t, v, screen)

	pressScrollbackKey(v, tcell.KeyUp)
	v.Draw(screen)
	if v.top != 0 {
		t.Fatalf("Up from first retained row top = %d, want Home 0", v.top)
	}
	assertScrollbackViewport(t, v, screen)

	pressScrollbackKey(v, tcell.KeyHome)
	v.Draw(screen)
	pressScrollbackKey(v, tcell.KeyDown)
	v.Draw(screen)
	if v.top != first {
		t.Fatalf("Down from Home top = %d, want first retained row %d", v.top, first)
	}
	assertScrollbackViewport(t, v, screen)
}

// TestScrollbackOverwrittenAnchorClampsToRetainedStart covers a reader whose
// anchored record is itself evicted: the viewport must land at the oldest
// retained record instead of keeping an index that no longer exists.
func TestScrollbackOverwrittenAnchorClampsToRetainedStart(t *testing.T) {
	v, screen := newScrollbackNavView(t)
	feedScrollback(v, scrollbackNavLines("line", 400), make([]string, 400), 0)
	v.Draw(screen)

	v.top = v.recordStartRow(150)
	v.follow = false
	v.Draw(screen)
	assertScrollbackViewport(t, v, screen)

	// Evict through record 299, including the anchored record 150.
	feedAndEvict(v, scrollbackNavLines("new", 400), 300)
	v.Draw(screen)
	first := v.leading + v.baseRow
	if v.top != first {
		t.Fatalf("overwritten anchor top = %d, want oldest retained row %d", v.top, first)
	}
	if v.follow {
		t.Fatal("eviction of the anchor unexpectedly resumed bottom following")
	}
	if got := v.rec(0).text; !strings.Contains(got, "line 0300") {
		t.Fatalf("oldest retained record = %q, want line 0300", got)
	}
	assertScrollbackViewport(t, v, screen)
}

// TestScrollbackRepeatedCompactionKeepsWindow checks that compacting the
// backing slice does not invalidate or shift the retained-relative widget
// window, and that the bounded backing array stays bounded.
func TestScrollbackRepeatedCompactionKeepsWindow(t *testing.T) {
	v, screen := newScrollbackNavView(t)
	chats := make([]string, 1000)
	feedScrollback(v, scrollbackNavLines("seed", 1000), chats, 0)
	v.Draw(screen)
	if !v.haveWindow || v.needWindow {
		t.Fatalf("initial window invalid: have=%v need=%v", v.haveWindow, v.needWindow)
	}

	compactions := 0
	for i := 0; i < 2100; i++ {
		beforeHead := v.head
		beforeStart, beforeEnd := v.winStart, v.winEnd
		next := v.base + int64(v.count())
		first := max(int64(0), next+1-maxOutputLines)
		v.sync(lineDelta{
			lines: []string{fmt.Sprintf("[#808080]live %04d[-]", i)},
			chats: []string{""},
			from:  next,
			upto:  next,
			first: first,
		})
		compacted := beforeHead > 0 && v.head == 0
		if compacted {
			compactions++
			if v.needWindow {
				t.Fatalf("compaction at %d invalidated a window that was after the evicted records", i)
			}
			if !v.haveWindow || v.winStart < 0 || v.winEnd > v.count() {
				t.Fatalf("compaction left invalid window [%d,%d) count=%d have=%v",
					v.winStart, v.winEnd, v.count(), v.haveWindow)
			}
			if v.winStart > beforeStart || v.winEnd > beforeEnd {
				t.Fatalf("compaction moved window up: [%d,%d) -> [%d,%d)",
					beforeStart, beforeEnd, v.winStart, v.winEnd)
			}
			v.Draw(screen)
			assertScrollbackViewport(t, v, screen)
			continue
		}
		v.Draw(screen)
	}
	if compactions < 2 {
		t.Fatalf("only %d compactions observed, want at least 2", compactions)
	}
	if got, limit := cap(v.records), 4*maxOutputLines; got > limit {
		t.Fatalf("records backing capacity %d exceeds %d", got, limit)
	}
}

// TestScrollbackResizeKeepsAnchorThroughEviction resizes while scrolled away
// and then evicts records before the anchor; the anchored content must not move.
func TestScrollbackResizeKeepsAnchorThroughEviction(t *testing.T) {
	v := newScrollbackView(false)
	v.SetBorder(true)
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(100, 30)
	v.SetRect(0, 0, 100, 30)

	lines := make([]string, 500)
	for i := range lines {
		lines[i] = fmt.Sprintf("[#808080]record %03d %s[-]", i, strings.Repeat("some words ", 5))
	}
	feedScrollback(v, lines, make([]string, len(lines)), 0)
	v.Draw(screen)
	v.top = v.recordStartRow(200)
	v.follow = false
	v.Draw(screen)
	index, _, ok := v.locate(v.top)
	if !ok {
		t.Fatal("anchor not located before resize")
	}

	screen.SetSize(64, 24)
	v.SetRect(0, 0, 64, 24)
	v.Draw(screen)
	newIndex, _, ok := v.locate(v.top)
	if !ok {
		t.Fatal("anchor not located after resize")
	}
	if newIndex != index {
		t.Fatalf("resize moved anchor from record %d to %d", index, newIndex)
	}
	before := scrollbackProjection(screen, 1, 1, 62, 22)

	feedAndEvict(v, scrollbackNavLines("new", 120), 100)
	v.Draw(screen)
	after := scrollbackProjection(screen, 1, 1, 62, 22)
	if diff := scrollbackDiff(before, after); diff != "" {
		t.Fatalf("eviction after resize moved anchored content: %s", diff)
	}
}

// TestScrollbackShortRetainedHistoryFollowsFromBeginning covers the saturated
// case where eviction leaves fewer retained rows than the pane height. The whole
// retained tail fits, so the viewport is the beginning of the widget text (the
// leading row) rather than an absolute row in the evicted gap.
func TestScrollbackShortRetainedHistoryFollowsFromBeginning(t *testing.T) {
	v, screen := newScrollbackNavView(t)
	feedScrollback(v, scrollbackNavLines("old", 500), make([]string, 500), 0)
	v.Draw(screen)

	feedAndEvict(v, scrollbackNavLines("tail", 5), 495)
	v.Draw(screen)
	if v.count() != 10 {
		t.Fatalf("retained records = %d, want 10", v.count())
	}
	if v.baseRow == 0 {
		t.Fatal("fixture did not retain evicted row metadata")
	}
	if v.leading+v.sumRows > int64(v.height) {
		t.Fatalf("fixture retained %d rows, pane height %d", v.leading+v.sumRows, v.height)
	}
	if v.top != 0 {
		t.Fatalf("short retained history top = %d, want beginning 0", v.top)
	}
	if !v.follow {
		t.Fatal("short retained history should still follow the end")
	}
	assertScrollbackViewport(t, v, screen)
}
