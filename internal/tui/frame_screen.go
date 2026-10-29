package tui

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// frameScreen is a tcell.Screen that restores final-frame comparison, which
// tview cannot get from tcell today.
//
// tview clears the screen and redraws every primitive on every frame
// (tview/application.go: screen.Clear before root.Draw). tcell's CellBuffer
// then invalidates the last-displayed value of every cell whose transient
// content changed (tcell/cell.go, cell.setDirty). A frame that ends with
// exactly the cells it started with therefore cannot be recognised as
// unchanged, and the whole non-blank screen is written to the terminal again
// for every keystroke.
//
// frameScreen keeps a logical cell buffer and a compact submitted snapshot, forwarding only
// cells that differ from the last submitted frame. The wrapped screen keeps its
// own diff against the physical display, so cells that did not change produce
// no terminal output at all. Clear/Fill stay logical and never touch the real
// screen, which is what makes an unchanged frame free.
//
// Everything else - event polling, mouse, cursor placement, cursor style, title,
// clipboard, tty access, resize events - is delegated unchanged, and Show/Sync
// remain on the drawing goroutine as tcell requires. Locked regions keep their
// tcell behaviour: the wrapped screen skips locked cells while drawing, and
// records their new content for the redraw that UnlockCell schedules.
type frameScreen struct {
	tcell.Screen

	mu    sync.Mutex
	work  tcell.CellBuffer // the frame currently being drawn
	shown []submittedCell  // no duplicate dirty/previous-style/width bookkeeping
	full  bool             // the next Show must repaint and resync everything
	done  bool

	initErr error
}

type submittedCell struct {
	text  string
	style tcell.Style
}

const printableASCII = " !\"#$%&'()*+,-./0123456789:;<=>?@ABCDEFGHIJKLMNOPQRSTUVWXYZ[\\]^_`abcdefghijklmnopqrstuvwxyz{|}~"

// newFrameScreen creates the terminal screen and wraps it in the final-frame
// diff adapter.
func newFrameScreen() (*frameScreen, error) {
	base, err := tcell.NewScreen()
	if err != nil {
		return nil, fmt.Errorf("tui: create terminal screen: %w", err)
	}
	return wrapFrameScreen(base), nil
}

// wrapFrameScreen gives an existing screen final-frame comparison. Tests wrap a
// simulation screen to assert what a user would see.
func wrapFrameScreen(base tcell.Screen) *frameScreen {
	screen := &frameScreen{Screen: base}
	screen.syncSizeLocked()
	return screen
}

// Init initialises the wrapped screen and remembers the failure: tview's
// SetScreen discards the error from Init, so Run reports it from InitError
// instead of silently drawing on a screen that never started.
func (s *frameScreen) Init() error {
	err := s.Screen.Init()
	s.mu.Lock()
	s.initErr = err
	if err == nil {
		s.done = false
		s.work, s.shown = tcell.CellBuffer{}, nil
		s.syncSizeLocked()
	}
	s.mu.Unlock()
	return err
}

// InitError returns the error Init recorded, if any.
func (s *frameScreen) InitError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.initErr
}

// Fini finalises the wrapped screen once.
func (s *frameScreen) Fini() {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}
	s.done = true
	s.mu.Unlock()
	s.Screen.Fini()
}

// Clear erases the logical frame. It deliberately does not clear the real
// screen: removed cells are simply submitted as blanks by the next Show.
func (s *frameScreen) Clear() { s.Fill(' ', tcell.StyleDefault) }

// Fill fills the logical frame with r and style.
func (s *frameScreen) Fill(r rune, style tcell.Style) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncSizeLocked()
	s.work.Fill(r, style)
}

// Put writes one grapheme cluster to the logical frame.
func (s *frameScreen) Put(x, y int, str string, style tcell.Style) (string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.work.Put(x, y, str, style)
}

// PutStr writes a string to the logical frame, clipping at its bounds.
func (s *frameScreen) PutStr(x, y int, str string) { s.PutStrStyled(x, y, str, tcell.StyleDefault) }

// PutStrStyled writes a styled string to the logical frame, clipping at its
// bounds the same way baseScreen does.
func (s *frameScreen) PutStrStyled(x, y int, str string, style tcell.Style) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncSizeLocked()
	cols, rows := s.work.Size()
	for str != "" && x < cols && y < rows {
		var width int
		str, width = s.work.Put(x, y, str, style)
		if width == 0 {
			break
		}
		x += width
	}
}

// SetCell writes a rune sequence (or a blank) to the logical frame.
func (s *frameScreen) SetCell(x, y int, style tcell.Style, ch ...rune) {
	if len(ch) > 0 {
		s.Put(x, y, string(ch), style)
		return
	}
	s.Put(x, y, " ", style)
}

// SetContent writes a primary rune plus combining runes to the logical frame.
func (s *frameScreen) SetContent(x, y int, mainc rune, combc []rune, style tcell.Style) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// tview uses this legacy API for every cell. CellBuffer.SetContent builds
	// an escaping rune slice/string even for ASCII. Slice immutable text for
	// that common case; Put still owns grapheme widths and ColorNone handling.
	if len(combc) == 0 && mainc >= ' ' && mainc <= '~' {
		i := int(mainc - ' ')
		s.work.Put(x, y, printableASCII[i:i+1], style)
		return
	}
	s.work.SetContent(x, y, mainc, combc, style)
}

// Get returns the logical content of a cell.
func (s *frameScreen) Get(x, y int) (string, tcell.Style, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncSizeLocked()
	return s.work.Get(x, y)
}

// GetContent returns the logical content of a cell in the deprecated shape.
func (s *frameScreen) GetContent(x, y int) (rune, []rune, tcell.Style, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncSizeLocked()
	return s.work.GetContent(x, y)
}

// Show submits only the cells that differ from the last submitted frame, then
// lets the wrapped screen compare them with what it last displayed.
func (s *frameScreen) Show() {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}
	s.syncSizeLocked()
	if s.full {
		s.pushAllLocked()
		s.full = false
		s.mu.Unlock()
		// Explicit redraw: the physical display may not match what the
		// wrapped screen believes it shows (resize, resume, style change).
		s.Screen.Sync()
		return
	}
	s.flushLocked()
	s.mu.Unlock()
	s.Screen.Show()
}

// Sync repaints everything, like tcell's Sync. It is used for explicit redraws
// (resize, resume, user request) where the terminal content cannot be trusted.
func (s *frameScreen) Sync() {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}
	s.syncSizeLocked()
	s.pushAllLocked()
	s.mu.Unlock()
	s.Screen.Sync()
}

// Resume re-engages the terminal; the content of the alternate screen is gone,
// so the next Show repaints all of it.
func (s *frameScreen) Resume() error {
	if err := s.Screen.Resume(); err != nil {
		return err
	}
	s.mu.Lock()
	s.full = true
	s.mu.Unlock()
	return nil
}

// SetSize resizes the window and the logical buffers, then forces an explicit
// redraw: every cell after a resize is suspect.
func (s *frameScreen) SetSize(w, h int) {
	s.Screen.SetSize(w, h)
	s.mu.Lock()
	s.syncSizeLocked()
	s.full = true
	s.mu.Unlock()
}

// SetStyle forwards the terminal default style. StyleDefault cells resolve
// against it while drawing, so a change invalidates the submitted frame.
func (s *frameScreen) SetStyle(style tcell.Style) {
	s.Screen.SetStyle(style)
	s.mu.Lock()
	s.full = true
	s.mu.Unlock()
}

// syncSizeLocked keeps the logical buffers the same size as the wrapped screen.
// CellBuffer.Resize preserves the overlapping content, so the submitted frame
// keeps matching what the wrapped screen resized to.
func (s *frameScreen) syncSizeLocked() {
	width, height := s.Screen.Size()
	w, h := s.work.Size()
	if w == width && h == height {
		return
	}
	shown := make([]submittedCell, width*height)
	for i := range shown {
		shown[i].text = " "
	}
	for y := 0; y < min(h, height); y++ {
		copy(shown[y*width:y*width+min(w, width)], s.shown[y*w:y*w+min(w, width)])
	}
	s.work.Resize(width, height)
	s.shown = shown
}

// flushLocked submits the cells whose final content differs from the last
// submitted frame. Wide glyphs are submitted as a single cell holding the whole
// grapheme cluster; the wrapped screen owns the second-column bookkeeping.
func (s *frameScreen) flushLocked() {
	width, height := s.work.Size()
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			str, style, _ := s.work.Get(x, y)
			prev := s.shown[y*width+x]
			if str == prev.text && style == prev.style {
				continue
			}
			s.submitLocked(x, y, str, style)
		}
	}
}

// pushAllLocked submits every cell of the logical frame.
func (s *frameScreen) pushAllLocked() {
	width, height := s.work.Size()
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			str, style, _ := s.work.Get(x, y)
			s.submitLocked(x, y, str, style)
		}
	}
}

// submitLocked forwards one cell and records it as submitted.
func (s *frameScreen) submitLocked(x, y int, str string, style tcell.Style) {
	s.Screen.Put(x, y, str, style)
	width, _ := s.work.Size()
	s.shown[y*width+x] = submittedCell{text: str, style: style}
}

// screenHost is the tview application plus the one piece of lifecycle state Run
// needs: whether the caller already installed its own screen. Tests and
// embedders do; Run then leaves that screen alone instead of replacing it with
// the diffing terminal screen.
type screenHost struct {
	*tview.Application

	external atomic.Bool
}

// newScreenHost creates the application used by New.
func newScreenHost() *screenHost {
	return &screenHost{Application: tview.NewApplication()}
}

// SetScreen records that the caller supplies the screen.
func (h *screenHost) SetScreen(screen tcell.Screen) *tview.Application {
	h.external.Store(true)
	return h.Application.SetScreen(screen)
}
