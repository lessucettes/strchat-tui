package tui

import (
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

// cellWrite is one cell submitted to the wrapped screen.
type cellWrite struct {
	x, y  int
	text  string
	style tcell.Style
}

// spyScreen records what the diff adapter submits to the real screen. It sits
// between the adapter and a simulation screen, so tests can assert both the
// amount of terminal-facing work and the visible result.
type spyScreen struct {
	tcell.Screen

	mu      sync.Mutex
	writes  []cellWrite
	shows   int
	syncs   int
	cursors []cellWrite
	locks   int
}

func newSpyScreen(t testing.TB) (*spyScreen, tcell.SimulationScreen) {
	t.Helper()
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatalf("init simulation screen: %v", err)
	}
	t.Cleanup(sim.Fini)
	return &spyScreen{Screen: sim}, sim
}

func (s *spyScreen) Put(x, y int, str string, style tcell.Style) (string, int) {
	s.record(x, y, str, style)
	return s.Screen.Put(x, y, str, style)
}

func (s *spyScreen) SetContent(x, y int, mainc rune, combc []rune, style tcell.Style) {
	s.record(x, y, string(append([]rune{mainc}, combc...)), style)
	s.Screen.SetContent(x, y, mainc, combc, style)
}

func (s *spyScreen) record(x, y int, text string, style tcell.Style) {
	s.mu.Lock()
	s.writes = append(s.writes, cellWrite{x: x, y: y, text: text, style: style})
	s.mu.Unlock()
}

func (s *spyScreen) Show() {
	s.mu.Lock()
	s.shows++
	s.mu.Unlock()
	s.Screen.Show()
}

func (s *spyScreen) Sync() {
	s.mu.Lock()
	s.syncs++
	s.mu.Unlock()
	s.Screen.Sync()
}

func (s *spyScreen) ShowCursor(x, y int) {
	s.mu.Lock()
	s.cursors = append(s.cursors, cellWrite{x: x, y: y})
	s.mu.Unlock()
	s.Screen.ShowCursor(x, y)
}

func (s *spyScreen) LockRegion(x, y, width, height int, lock bool) {
	s.mu.Lock()
	s.locks++
	s.mu.Unlock()
	s.Screen.LockRegion(x, y, width, height, lock)
}

func (s *spyScreen) reset() {
	s.mu.Lock()
	s.writes = nil
	s.shows, s.syncs, s.locks = 0, 0, 0
	s.cursors = nil
	s.mu.Unlock()
}

func (s *spyScreen) snapshot() (writes []cellWrite, shows, syncs, locks int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]cellWrite(nil), s.writes...), s.shows, s.syncs, s.locks
}

func (s *spyScreen) writeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.writes)
}

// visibleCell is one grid cell as it is actually displayed or as the logical
// frame holds it.
type visibleCell struct {
	text  string
	style tcell.Style
}

// grid is the displayed or logical cells, row by row.
type grid [][]visibleCell

// projection collapses a cell grid to what a terminal shows: the cell after a
// two-column cell displays no content of its own.
func projection(width, height int, get func(x, y int) (string, tcell.Style, int)) grid {
	rows := make([][]visibleCell, height)
	for y := 0; y < height; y++ {
		row := make([]visibleCell, 0, width)
		for x := 0; x < width; x++ {
			text, style, w := get(x, y)
			if w == 2 && x+1 >= width {
				// tcell renders a wide glyph that does not fit in the last
				// column as a single space (drawCell's width clamp).
				text, w = " ", 1
			}
			row = append(row, visibleCell{text: text, style: style})
			if w == 2 {
				x++
			}
		}
		rows[y] = row
	}
	return rows
}

func (p grid) String() string {
	var b strings.Builder
	for _, row := range p {
		for _, cell := range row {
			b.WriteString(cell.text)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (p grid) equal(other grid) (bool, string) {
	if len(p) != len(other) {
		return false, fmt.Sprintf("row counts differ: %d != %d", len(p), len(other))
	}
	for y := range p {
		if len(p[y]) != len(other[y]) {
			return false, fmt.Sprintf("row %d lengths differ: %d != %d", y, len(p[y]), len(other[y]))
		}
		for x := range p[y] {
			if p[y][x].text != other[y][x].text {
				return false, fmt.Sprintf("cell (%d,%d) text %q != %q\nwant:\n%s\ngot:\n%s",
					x, y, p[y][x].text, other[y][x].text, p, other)
			}
			if p[y][x].style != other[y][x].style {
				return false, fmt.Sprintf("cell (%d,%d) style %v != %v", x, y, p[y][x].style, other[y][x].style)
			}
		}
	}
	return true, ""
}

// frameCell is one logical write: text is the whole grapheme cluster written at
// the cell.
type frameCell struct {
	x, y  int
	text  string
	style tcell.Style
}

func styleFor(i int) tcell.Style {
	switch i % 4 {
	case 1:
		return tcell.StyleDefault.Foreground(tcell.ColorRed)
	case 2:
		return tcell.StyleDefault.Background(tcell.ColorBlue).Reverse(true)
	case 3:
		return tcell.StyleDefault.Foreground(tcell.ColorGreen).Underline(true)
	default:
		return tcell.StyleDefault
	}
}

// applyFrame draws a frame the way tview does: clear everything, write the
// cells, show.
func applyFrame(screen tcell.Screen, cells []frameCell) {
	screen.Clear()
	for _, c := range cells {
		runes := []rune(c.text)
		screen.SetContent(c.x, c.y, runes[0], runes[1:], c.style)
	}
	screen.Show()
}

// screenProjection returns what the terminal physically shows for a simulation
// screen: its front cells, not the logical buffer it was given. The column a wide
// glyph consumes is skipped, using the width the screen recorded for that cell.
func screenProjection(sim tcell.SimulationScreen) grid {
	cells, w, h := sim.GetContents()
	return projection(w, h, func(x, y int) (string, tcell.Style, int) {
		_, _, _, width := sim.GetContent(x, y)
		text := string(cells[y*w+x].Runes)
		if text == "" {
			text = " "
		}
		return text, cells[y*w+x].Style, width
	})
}

// screenText is the text a user sees on the simulation screen.
func screenText(sim tcell.SimulationScreen) string {
	return screenProjection(sim).String()
}

// TestFrameScreenUnchangedFrameWritesNothing is the defect this type exists for:
// a frame that ends with the same cells it started with must not reach the
// terminal.
func TestFrameScreenUnchangedFrameWritesNothing(t *testing.T) {
	spy, sim := newSpyScreen(t)
	sim.SetSize(40, 10)
	screen := wrapFrameScreen(spy)

	frame := make([]frameCell, 0, 120)
	for i := 0; i < 120; i++ {
		frame = append(frame, frameCell{x: i % 40, y: i / 40, text: "x", style: styleFor(i)})
	}
	applyFrame(screen, frame)
	if got := spy.writeCount(); got != len(frame) {
		t.Fatalf("first frame submitted %d cells, want %d", got, len(frame))
	}

	spy.reset()
	for i := 0; i < 5; i++ {
		applyFrame(screen, frame)
	}
	if writes, shows, _, _ := spy.snapshot(); len(writes) != 0 {
		t.Fatalf("unchanged frames submitted %d cells, want 0: %+v", len(writes), writes)
	} else if shows != 5 {
		t.Fatalf("Show calls = %d, want 5", shows)
	}
	if ok, _ := mustProjection(t, screen).equal(screenProjection(sim)); !ok {
		t.Fatal("visible content changed while nothing was submitted")
	}
}

func mustProjection(t *testing.T, screen *frameScreen) grid {
	t.Helper()
	w, h := screen.Size()
	return projection(w, h, func(x, y int) (string, tcell.Style, int) {
		return screen.Get(x, y)
	})
}

// TestFrameScreenWideTransitions checks the cases where a final-frame diff can
// leave a stale half-glyph or a hidden character: wide to narrow, narrow to
// wide, writes into the second column of a wide glyph, wide at the last column,
// combining marks and ZWJ emoji.
func TestFrameScreenWideTransitions(t *testing.T) {
	wide := "世"
	emoji := "👍"
	family := "👩‍👩‍👧‍👦"
	combining := "e\u0301"

	cases := []struct {
		name   string
		width  int
		frames [][]frameCell
	}{
		{
			name:  "wide then narrow at the same cell",
			width: 6,
			frames: [][]frameCell{
				{{x: 0, y: 0, text: wide}},
				{{x: 0, y: 0, text: "a"}},
				{{x: 0, y: 0, text: wide}},
				{{x: 0, y: 0, text: "b"}},
			},
		},
		{
			name:  "narrow pair replaced by wide",
			width: 6,
			frames: [][]frameCell{
				{{x: 0, y: 0, text: "a"}, {x: 1, y: 0, text: "b"}},
				{{x: 0, y: 0, text: wide}},
				{{x: 0, y: 0, text: "a"}, {x: 1, y: 0, text: "b"}},
			},
		},
		{
			name:  "write into the second column of a wide glyph",
			width: 6,
			frames: [][]frameCell{
				{{x: 0, y: 0, text: wide}},
				{{x: 0, y: 0, text: wide}, {x: 1, y: 0, text: "b"}},
				{{x: 0, y: 0, text: wide}},
				{{x: 0, y: 0, text: "a"}, {x: 1, y: 0, text: wide}},
				{{x: 0, y: 0, text: "a"}},
			},
		},
		{
			name:  "wide at the last column",
			width: 4,
			frames: [][]frameCell{
				{{x: 3, y: 0, text: wide}},
				{{x: 3, y: 0, text: wide}},
				{{x: 3, y: 0, text: "z"}},
				{{x: 2, y: 0, text: wide}, {x: 3, y: 0, text: wide}},
			},
		},
		{
			name:  "combining and ZWJ sequences",
			width: 8,
			frames: [][]frameCell{
				{{x: 0, y: 0, text: combining}, {x: 1, y: 0, text: family}, {x: 4, y: 0, text: emoji}},
				{{x: 0, y: 0, text: "e"}, {x: 1, y: 0, text: family}, {x: 4, y: 0, text: "x"}},
				{{x: 0, y: 0, text: combining}, {x: 2, y: 0, text: emoji}},
				{{x: 1, y: 0, text: emoji}, {x: 3, y: 0, text: family}},
			},
		},
		{
			name:  "wide rows cleared and rebuilt",
			width: 8,
			frames: [][]frameCell{
				{{x: 1, y: 0, text: wide}, {x: 3, y: 0, text: emoji}, {x: 5, y: 0, text: "k"}},
				{},
				{{x: 1, y: 0, text: wide}, {x: 3, y: 0, text: emoji}, {x: 5, y: 0, text: "k"}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy, sim := newSpyScreen(t)
			sim.SetSize(tc.width, 3)
			screen := wrapFrameScreen(spy)

			nativeSim := tcell.NewSimulationScreen("UTF-8")
			if err := nativeSim.Init(); err != nil {
				t.Fatalf("init native simulation screen: %v", err)
			}
			t.Cleanup(nativeSim.Fini)
			nativeSim.SetSize(tc.width, 3)

			for i, frame := range tc.frames {
				applyFrame(screen, frame)
				applyFrame(nativeSim, frame)

				want := mustProjection(t, screen)
				if ok, diff := want.equal(screenProjection(sim)); !ok {
					t.Fatalf("frame %d: displayed content differs from the logical frame: %s", i, diff)
				}
				// tcell itself can resurrect a character hidden under a wide
				// glyph; compare with the native screen only when no write
				// landed in a hidden column of the frame.
				if !writesHiddenColumn(screen, frame) {
					if ok, diff := screenProjection(nativeSim).equal(screenProjection(sim)); !ok {
						t.Fatalf("frame %d: display differs from an unadapted screen: %s", i, diff)
					}
				}
			}
		})
	}
}

// writesHiddenColumn reports whether a frame writes into the column that a wide
// glyph to its left already occupies. tcell keeps that unused cell content in
// its buffer, so a full repaint can reveal it later; the diff adapter
// deliberately submits the real final content instead.
func writesHiddenColumn(screen *frameScreen, frame []frameCell) bool {
	for _, c := range frame {
		if _, _, width := screen.Get(c.x-1, c.y); width == 2 {
			return true
		}
	}
	return false
}

// TestFrameScreenRandomizedFrames drives many frames of every cell class and
// checks that the displayed grid always equals the logical frame, that cells
// which did not change are never resubmitted, and that a repeat of the previous
// frame submits nothing.
func TestFrameScreenRandomizedFrames(t *testing.T) {
	const (
		width  = 24
		height = 8
		frames = 200
	)
	corpus := []string{
		" ", "a", "Z", "0", "~", "é", "e\u0301", "世", "界", "あ",
		"👍", "👩‍👩‍👧‍👦", "🇷🇺", "→", "·",
	}
	spy, sim := newSpyScreen(t)
	sim.SetSize(width, height)
	screen := wrapFrameScreen(spy)

	rng := rand.New(rand.NewSource(20261023))
	var previous []frameCell
	for i := 0; i < frames; i++ {
		cells := make([]frameCell, 0, 2*width)
		for n := rng.Intn(3 * width); n > 0; n-- {
			cells = append(cells, frameCell{
				x:     rng.Intn(width),
				y:     rng.Intn(height),
				text:  corpus[rng.Intn(len(corpus))],
				style: styleFor(rng.Intn(4)),
			})
		}
		if i%17 == 0 {
			cells = previous // identical repeated frame
		}
		before := logicalCells(screen)
		spy.reset()
		applyFrame(screen, cells)

		if ok, diff := mustProjection(t, screen).equal(screenProjection(sim)); !ok {
			t.Fatalf("frame %d: displayed content differs from the logical frame: %s", i, diff)
		}
		if sameFrame(cells, previous) {
			if got := spy.writeCount(); got != 0 {
				t.Fatalf("frame %d repeated the previous frame but submitted %d cells", i, got)
			}
		}
		// Exactly the cells whose final content or style changed may be
		// submitted: no unchanged cell may cost terminal output.
		if got, want := spy.writeCount(), changedCells(before, logicalCells(screen)); got != want {
			t.Fatalf("frame %d submitted %d cells, want %d (writes: %d)", i, got, want, len(cells))
		}
		previous = cells
	}
}

// logicalCells returns the logical frame cell by cell (including the hidden
// column of a wide glyph, which is part of the buffer).
func logicalCells(screen *frameScreen) []visibleCell {
	w, h := screen.Size()
	cells := make([]visibleCell, 0, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			text, style, _ := screen.Get(x, y)
			cells = append(cells, visibleCell{text: text, style: style})
		}
	}
	return cells
}

// changedCells counts the cells that differ between two logical frames.
func changedCells(before, after []visibleCell) int {
	n := 0
	for i := range after {
		if i >= len(before) || before[i] != after[i] {
			n++
		}
	}
	return n
}

func sameFrame(a, b []frameCell) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestFrameScreenSyncRepaintsEverything covers explicit redraw paths: Sync, a
// size change and resume all have to repaint the physical screen even though
// the logical frame did not change.
func TestFrameScreenSyncRepaintsEverything(t *testing.T) {
	spy, sim := newSpyScreen(t)
	sim.SetSize(30, 6)
	screen := wrapFrameScreen(spy)
	frame := []frameCell{{x: 0, y: 0, text: "a"}, {x: 1, y: 0, text: "世"}}
	applyFrame(screen, frame)

	spy.reset()
	screen.Sync()
	writes, _, syncs, _ := spy.snapshot()
	if syncs != 1 {
		t.Fatalf("Sync calls = %d, want 1", syncs)
	}
	if len(writes) != 30*6 {
		t.Fatalf("Sync repainted %d cells, want %d", len(writes), 30*6)
	}
	if ok, diff := mustProjection(t, screen).equal(screenProjection(sim)); !ok {
		t.Fatalf("content after Sync: %s", diff)
	}

	// After a Sync the submitted frame matches again, so a repeated frame is
	// free.
	spy.reset()
	applyFrame(screen, frame)
	if got := spy.writeCount(); got != 0 {
		t.Fatalf("frame after Sync submitted %d cells, want 0", got)
	}

	// A resize repaints and preserves content.
	spy.reset()
	screen.SetSize(40, 10)
	applyFrame(screen, frame)
	writes, _, _, _ = spy.snapshot()
	if len(writes) < 40*10 {
		t.Fatalf("resize submitted %d cells, want a full repaint of %d", len(writes), 40*10)
	}
	if ok, diff := mustProjection(t, screen).equal(screenProjection(sim)); !ok {
		t.Fatalf("content after resize: %s", diff)
	}

	// Resume must repaint too: the terminal content is gone.
	spy.reset()
	if err := screen.Resume(); err != nil {
		t.Fatalf("resume: %v", err)
	}
	applyFrame(screen, frame)
	writes, _, syncs, _ = spy.snapshot()
	if syncs != 1 || len(writes) != 40*10 {
		t.Fatalf("resume: %d writes, %d syncs; want full repaint and one sync", len(writes), syncs)
	}
}

// TestFrameScreenConcurrentUse runs the buffer under the race detector: drawing
// and reading may come from different goroutines.
func TestFrameScreenConcurrentUse(t *testing.T) {
	spy, sim := newSpyScreen(t)
	sim.SetSize(16, 4)
	screen := wrapFrameScreen(spy)

	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				screen.Put(g*3+i%3, i%4, "x", tcell.StyleDefault)
				_, _, _ = screen.Get(0, 0)
				screen.Size()
				screen.Show()
			}
		}(g)
	}
	wg.Wait()
}

// TestRunKeepsCallerScreen proves the lifecycle hook does not replace a screen
// the caller installed, which is what every functional test here relies on.
func TestRunKeepsCallerScreen(t *testing.T) {
	s := newSimTUI(t, 100, 30)
	if !s.ui.app.external.Load() {
		t.Fatal("SetScreen did not record the caller-provided screen")
	}
	s.events <- testState()
	s.events <- testMessage(1)
	waitFor(t, 3*time.Second, func() bool {
		return s.matches(func() bool { return strings.Contains(screenText(s.screen), "message 1") })
	})
}
