package tui

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/lessucettes/strchat-tui/internal/client"
)

const (
	artWidth    = 16
	artMinWidth = 120
	artInterval = 125 * time.Millisecond
)

var artNames = client.ArtScenes()

type artTone uint8

const (
	artMuted artTone = iota
	artAccent
	artLeaf
	artFlower
	artWater
	artWarm
	artDanger
)

type artCell struct {
	ch   rune
	tone artTone
}

// Scenes only see a clipped ASCII canvas, never widgets or client state.
type artCanvas struct {
	w, h  int
	cells []artCell
}

func (c *artCanvas) reset(w, h int) {
	c.w, c.h = max(0, min(w, artWidth)), max(0, min(h, 256))
	n := c.w * c.h
	if cap(c.cells) < n {
		c.cells = make([]artCell, n)
	} else {
		c.cells = c.cells[:n]
		clear(c.cells)
	}
}

func (c *artCanvas) put(x, y int, ch rune, tone artTone) {
	if x >= 0 && x < c.w && y >= 0 && y < c.h && ch >= ' ' && ch <= '~' {
		c.cells[y*c.w+x] = artCell{ch, tone}
	}
}

func (c *artCanvas) text(x, y int, text string, tone artTone) {
	for _, ch := range text {
		c.put(x, y, ch, tone)
		x++
	}
}

func (c *artCanvas) line(x0, y0, x1, y1 int, ch rune, tone artTone) {
	dx, dy := absArt(x1-x0), -absArt(y1-y0)
	sx, sy := 1, 1
	if x0 > x1 {
		sx = -1
	}
	if y0 > y1 {
		sy = -1
	}
	err := dx + dy
	for {
		c.put(x0, y0, ch, tone)
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

func absArt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func artRand(seed, salt uint64) *rand.Rand {
	return rand.New(rand.NewPCG(seed, salt))
}

type artScene interface {
	draw(*artCanvas, time.Duration)
}

type artSceneFunc func(*artCanvas, time.Duration)

func (f artSceneFunc) draw(c *artCanvas, elapsed time.Duration) { f(c, elapsed) }

// All fields, including scene simulations, belong to the drawing event loop.
type artPane struct {
	*tview.Box
	name      string
	selection string
	revision  uint64
	scene     artScene
	canvas    artCanvas
	theme     *theme
	elapsed   time.Duration
	lastDraw  time.Time
	shown     bool
}

func newArtPane(th *theme) *artPane {
	p := &artPane{Box: tview.NewBox(), theme: th}
	p.selectScene(client.DefaultArt)
	return p
}

func (p *artPane) selectScene(name string) {
	p.selection = name
	if name == "random" {
		name = artNames[rand.IntN(len(artNames))]
	}
	p.name = name
	p.scene = nil
	seed := rand.Uint64()
	if name != "off" {
		p.scene = newNatureScene(name, seed)
		if p.scene == nil {
			p.scene = newStoryScene(name, seed)
		}
	}
	p.elapsed, p.lastDraw = 0, time.Time{}
}

func (p *artPane) setShown(shown bool) {
	if p.shown != shown {
		p.shown = shown
		p.lastDraw = time.Time{}
	}
}

func (p *artPane) Draw(screen tcell.Screen) {
	p.Box.DrawForSubclass(screen, p)
	if p.scene == nil || !p.shown {
		return
	}
	x, y, w, h := p.GetInnerRect()
	if w <= 0 || h <= 0 {
		return
	}
	now := time.Now()
	if !p.lastDraw.IsZero() {
		// A stalled or hidden terminal must not trigger unbounded catch-up work.
		p.elapsed += min(now.Sub(p.lastDraw), 250*time.Millisecond)
	}
	p.lastDraw = now
	p.canvas.reset(w, h)
	p.scene.draw(&p.canvas, p.elapsed)
	th := p.theme
	colors := [...]tcell.Color{th.borderColor, th.titleColor, th.titleColor, th.logWarnColor, th.logInfoColor, th.logWarnColor, th.logErrorColor}
	for row := 0; row < p.canvas.h; row++ {
		for col := 0; col < p.canvas.w; col++ {
			cell := p.canvas.cells[row*p.canvas.w+col]
			if cell.ch == 0 || cell.ch == ' ' {
				continue
			}
			screen.Put(x+col, y+row, string(cell.ch), tcell.StyleDefault.Foreground(colors[cell.tone]).Background(th.backgroundColor))
		}
	}
}

// The ornament is not a navigation target and never delegates keyboard/mouse focus.
func (p *artPane) Focus(func(tview.Primitive))                                {}
func (p *artPane) HasFocus() bool                                             { return false }
func (p *artPane) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) { return nil }
func (p *artPane) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	// Grid calls every child's handler without a nil check, even when the
	// pointer is outside that child. Ignore events without taking focus.
	return func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
		return false, nil
	}
}

func (t *tui) handleArt(payload string) {
	name := strings.TrimSpace(payload)
	if name == "" {
		t.applyLocalEvent(client.DisplayEvent{Type: "INFO", Content: fmt.Sprintf("ASCII scenes (mode: %s; current: %s):\n%s\nUsage: /art <scene|random|off> (Alias: /a). Select again for a new variation.\nSelection is saved. Default: random at startup.\nHidden in small windows and maximized panes.", t.art.selection, t.art.name, strings.Join(artNames, ", "))})
		return
	}
	valid := name == "off" || name == "random"
	for _, candidate := range artNames {
		valid = valid || name == candidate
	}
	if !valid {
		t.bell()
		t.applyLocalEvent(client.DisplayEvent{Type: "ERROR", Content: "Unknown ASCII scene. Use /art to list scenes."})
		return
	}
	t.submit(client.UserAction{Type: "SET_ART", Payload: name})
}

// restoreArt runs on the event loop after startup or a successful save. Track
// acknowledgements, not names: selecting the same mode requests a new variation.
func (t *tui) restoreArt(name string, revision uint64) {
	if t.art.revision == revision {
		return
	}
	t.art.revision = revision
	t.art.selectScene(name)
	t.layoutDirty = true
	t.markDirty()
}

// Called only by the event loop. The redraw worker sees an atomic flag and a
// coalesced notification, never the primitive or mutable simulation state.
func (t *tui) syncArt() {
	shown := t.plan.art && t.art.scene != nil && t.maximizeModeOf() == maximizeNone
	t.art.theme = t.theme
	t.art.SetBackgroundColor(t.theme.backgroundColor)
	t.art.setShown(shown)
	if t.artRunning.Swap(shown) != shown {
		select {
		case t.artWake <- struct{}{}:
		default:
		}
	}
}

// newNatureScene owns the generative nature decorations. It returns nil for
// names handled by the story scenes so artPane can fall back deterministically.
func newNatureScene(name string, seed uint64) artScene {
	switch name {
	case "herbarium":
		return newHerbariumScene(seed)
	case "interference":
		return newInterferenceScene(seed)
	case "windmill":
		return newWindmillScene(seed)
	case "rain":
		return newRainScene(seed)
	}
	return nil
}

// natHash is a splitmix-style mixer for stable per-cell variation. Everything
// derives from the scene seed, so any frame can be redrawn independently.
func natHash(seed uint64, a, b int) uint64 {
	v := seed
	v ^= uint64(uint32(a+8192)) * 0x9e3779b97f4a7c15
	v ^= uint64(uint32(b+8192)) * 0xc2b2ae3d27d4eb4f
	v += 0x165667b19e3779f9
	v = (v ^ (v >> 30)) * 0xbf58476d1ce4e5b9
	v = (v ^ (v >> 27)) * 0x94d049bb133111eb
	return v ^ (v >> 31)
}

func natUnit(seed uint64, a, b int) float64 {
	return float64(natHash(seed, a, b)>>11) / float64(uint64(1)<<53)
}

func natFrac(v float64) float64 {
	return v - math.Floor(v)
}

func natClampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// natSoft writes only into empty cells, which keeps stems readable when
// foliage from a neighbouring stalk overlaps them.
func natSoft(c *artCanvas, x, y int, ch rune, tone artTone) {
	if c == nil || x < 0 || x >= c.w || y < 0 || y >= c.h {
		return
	}
	if c.cells[y*c.w+x].ch == 0 || c.cells[y*c.w+x].ch == ' ' {
		c.put(x, y, ch, tone)
	}
}

// ---------------------------------------------------------------------------
// Herbarium: a potted specimen plate. Four seed-selected families share the
// stem renderer and differ in leaf shape, blossom and habit.
// ---------------------------------------------------------------------------

type natHerbStalk struct {
	base      float64 // x as a fraction of the pot opening
	tip       float64 // x as a fraction of the pane width
	height    float64 // fraction of the plant area
	bend      float64
	sway      float64
	phase     float64
	nodeShift float64
	style     int // 0 narrow leaf, 2 fern, 3 bare flower stem
	bloom     int // 0 none, 1 aster, 2 bell, 3 spike, 4 bud
}

type natHerbarium struct {
	seed    uint64
	species int
	stalks  []natHerbStalk
	vine    bool
}

func newHerbariumScene(seed uint64) artScene {
	rng := artRand(seed, 0x68657262)
	h := &natHerbarium{seed: seed, species: rng.IntN(4)}
	switch h.species {
	case 0: // fernery: three arching fronds
		h.stalks = []natHerbStalk{
			{base: 0.400, tip: 0.150, height: 0.58, bend: -0.06, sway: 0.24, phase: rng.Float64() * 6.28, nodeShift: rng.Float64() * 0.5, style: 2},
			{base: 0.500, tip: 0.470, height: 0.92, bend: 0.04, sway: 0.18, phase: rng.Float64() * 6.28, nodeShift: rng.Float64() * 0.5, style: 2},
			{base: 0.600, tip: 0.850, height: 0.68, bend: 0.06, sway: 0.26, phase: rng.Float64() * 6.28, nodeShift: rng.Float64() * 0.5, style: 2},
		}
	case 1: // flowering spray
		h.stalks = []natHerbStalk{
			{base: 0.420, tip: 0.220, height: 0.58, bend: -0.05, sway: 0.22, phase: rng.Float64() * 6.28, nodeShift: rng.Float64() * 0.5, style: 3, bloom: 2},
			{base: 0.500, tip: 0.470, height: 0.92, bend: 0.02, sway: 0.16, phase: rng.Float64() * 6.28, nodeShift: rng.Float64() * 0.5, style: 0, bloom: 1},
			{base: 0.580, tip: 0.800, height: 0.66, bend: 0.06, sway: 0.22, phase: rng.Float64() * 6.28, nodeShift: rng.Float64() * 0.5, style: 3, bloom: 3},
		}
	case 2: // slender stalks with a small hanging vine
		h.vine = true
		h.stalks = []natHerbStalk{
			{base: 0.400, tip: 0.330, height: 0.92, bend: 0.03, sway: 0.18, phase: rng.Float64() * 6.28, nodeShift: rng.Float64() * 0.4, style: 0},
			{base: 0.620, tip: 0.700, height: 0.62, bend: 0.05, sway: 0.22, phase: rng.Float64() * 6.28, nodeShift: rng.Float64() * 0.4, style: 3, bloom: 4},
		}
	default: // mixed border
		h.stalks = []natHerbStalk{
			{base: 0.410, tip: 0.180, height: 0.60, bend: -0.05, sway: 0.22, phase: rng.Float64() * 6.28, nodeShift: rng.Float64() * 0.4, style: 3, bloom: 2},
			{base: 0.510, tip: 0.520, height: 0.90, bend: 0.03, sway: 0.18, phase: rng.Float64() * 6.28, nodeShift: rng.Float64() * 0.4, style: 0, bloom: 1},
			{base: 0.600, tip: 0.820, height: 0.56, bend: 0.06, sway: 0.24, phase: rng.Float64() * 6.28, nodeShift: rng.Float64() * 0.4, style: 3, bloom: 4},
		}
	}
	return artSceneFunc(h.draw)
}

func natHerbPotBounds(w int) (x, width int) {
	width = min(7, w)
	return (w - width) / 2, width
}

func (s natHerbStalk) xAt(u, w, t float64) float64 {
	potX, potW := natHerbPotBounds(int(w))
	root := float64(int(w) / 2)
	if potW >= 5 {
		root = float64(potX+1) + s.base*float64(potW-3)
	}
	x := root + (s.tip*w-root)*u + s.bend*w*math.Sin(math.Pi*u)
	x += s.sway * math.Sin(t*0.40+s.phase) * u * u
	return x
}

func (h *natHerbarium) draw(c *artCanvas, elapsed time.Duration) {
	if c == nil || c.w <= 0 || c.h <= 0 {
		return
	}
	t := elapsed.Seconds()
	potRows := 3
	if c.h >= 16 {
		potRows = 4
	}
	if c.h < 6 {
		potRows = 2
	}
	potTop := c.h - potRows
	if potTop < 1 {
		potTop = 1
	}
	potX, potW := natHerbPotBounds(c.w)

	for i := range h.stalks {
		h.drawStalk(c, &h.stalks[i], potTop, t)
	}
	for i := range h.stalks {
		h.drawStalkBloom(c, &h.stalks[i], potTop, t)
	}
	if h.vine && c.h >= 13 {
		h.drawVine(c, potTop, t)
	}
	// Paint the small foliage after every stem so later stalks do not erase it.
	for i := range h.stalks {
		h.drawStalkLeaves(c, &h.stalks[i], potTop, t)
	}
	h.drawPot(c, potX, potTop, potW, potRows)
}

func (h *natHerbarium) drawStalk(c *artCanvas, s *natHerbStalk, potTop int, t float64) {
	if potTop < 2 {
		return
	}
	w := float64(c.w)
	tipY := potTop - int(math.Round(float64(potTop)*s.height))
	tipY = natClampInt(tipY, 0, potTop-1)
	span := potTop - 1 - tipY
	if span < 2 {
		return
	}

	prevX, first := 0, true
	for y := tipY; y < potTop; y++ {
		// Anchor the last visible cell above the rim, not a hidden cell under it.
		u := float64(potTop-1-y) / float64(span)
		x := int(math.Round(s.xAt(u, w, t)))
		ch := '|'
		if !first {
			if x > prevX {
				ch = '\\'
			} else if x < prevX {
				ch = '/'
			}
			// Short panes can move several columns per row. Join the cells
			// instead of leaving disconnected pieces of a steep stalk.
			if absArt(x-prevX) > 1 {
				c.line(prevX, y-1, x, y, ch, artMuted)
			}
		}
		c.put(x, y, ch, artMuted)
		prevX, first = x, false
	}
}

func (h *natHerbarium) drawStalkLeaves(c *artCanvas, s *natHerbStalk, potTop int, t float64) {
	if potTop < 2 {
		return
	}
	w := float64(c.w)
	tipY := natClampInt(potTop-int(math.Round(float64(potTop)*s.height)), 0, potTop-1)
	span := potTop - 1 - tipY
	if span < 2 {
		return
	}
	nodes := natClampInt(span/6, 2, 4)
	if s.style == 2 {
		nodes = natClampInt(span/3, 3, 14)
	}
	if s.style == 3 {
		nodes = 1
	}
	for i := 1; i <= nodes; i++ {
		u := 0.12 + 0.80*(float64(i)+s.nodeShift)/float64(nodes+1)
		if u > 0.95 {
			u = 0.95
		}
		y := potTop - 1 - int(math.Round(float64(span)*u))
		// Attach at the actual rasterized stem row, not the unrounded node.
		x := int(math.Round(s.xAt(float64(potTop-1-y)/float64(span), w, t)))
		side := 1
		if i%2 == 0 {
			side = -1
		}
		switch s.style {
		case 2:
			natFernPinna(c, x, y, side)
			if i%3 == 0 {
				natFernPinna(c, x, y, -side)
			}
		case 3:
			// Flower stems stay nearly bare so the blooms carry the shape.
			if i == 1 {
				outward := 1
				if s.tip < 0.5 {
					outward = -1
				}
				natNarrowLeaf(c, x, y, outward, artLeaf)
			}
		default:
			natNarrowLeaf(c, x, y, side, artLeaf)
			if i == 1 || i == nodes/2 {
				natNarrowLeaf(c, x, y, -side, artLeaf)
			}
		}
	}

	tipX := int(math.Round(s.xAt(1, w, t)))
	if s.style == 2 && tipY+2 < potTop {
		natSoft(c, tipX+1, tipY+1, ',', artLeaf)
		natSoft(c, tipX-1, tipY+2, '\'', artLeaf)
	}
}

func (h *natHerbarium) drawStalkBloom(c *artCanvas, s *natHerbStalk, potTop int, t float64) {
	if s.bloom == 0 || potTop < 3 {
		return
	}
	tipY := potTop - int(math.Round(float64(potTop)*s.height))
	tipY = natClampInt(tipY, 1, potTop-2)
	tipX := int(math.Round(s.xAt(1, float64(c.w), t)))
	natBloom(c, tipX, tipY-1, s.bloom)
}

func natFernPinna(c *artCanvas, x, y, dir int) {
	natSoft(c, x+dir, y, ',', artLeaf)
	natSoft(c, x+2*dir, y-1, '\'', artLeaf)
}

func natNarrowLeaf(c *artCanvas, x, y, dir int, tone artTone) {
	if dir > 0 {
		natSoft(c, x+1, y, '/', tone)
		natSoft(c, x+2, y-1, ',', tone)
	} else {
		natSoft(c, x-1, y, '\\', tone)
		natSoft(c, x-2, y-1, ',', tone)
	}
}

func natBloom(c *artCanvas, x, y, kind int) {
	switch kind {
	case 1: // starry aster
		c.put(x, y, '*', artFlower)
		c.put(x-1, y, '(', artAccent)
		c.put(x+1, y, ')', artAccent)
	case 2: // nodding bell
		c.put(x, y, 'o', artFlower)
		c.put(x-1, y-1, '.', artAccent)
		c.put(x+1, y-1, '.', artAccent)
	case 3: // flower spike
		c.put(x, y, '*', artFlower)
		c.put(x, y-1, '\'', artAccent)
		c.put(x-1, y-2, '\'', artAccent)
		c.put(x+1, y-2, '.', artAccent)
	case 4: // bud
		c.put(x, y, 'o', artFlower)
		c.put(x, y+1, '\'', artMuted)
	}
}

func (h *natHerbarium) drawVine(c *artCanvas, potTop int, t float64) {
	// The vine belongs to this pot, not to an invisible support above the pane.
	s := natHerbStalk{base: 0.52, tip: 0.82, height: 0.72, bend: 0.06, sway: 0.45, phase: float64(h.seed % 11)}
	h.drawStalk(c, &s, potTop, t)
	tipY := potTop - int(math.Round(float64(potTop)*s.height))
	span := potTop - 1 - tipY
	if span < 2 {
		return
	}
	for y := tipY; y < potTop-1; y++ {
		if (potTop-1-y)%3 == 2 {
			u := float64(potTop-1-y) / float64(span)
			x := int(math.Round(s.xAt(u, float64(c.w), t)))
			natSoft(c, x-1, y, ')', artLeaf)
			natSoft(c, x-2, y-1, ',', artLeaf)
		}
	}
}

func (h *natHerbarium) drawPot(c *artCanvas, x, top, wid, rows int) {
	if wid < 5 || rows < 2 {
		cx := c.w / 2
		for y := top; y < c.h && y < top+rows+1; y++ {
			c.put(cx, y, '|', artFlower)
		}
		return
	}
	right := x + wid - 1
	c.put(x, top, '\\', artFlower)
	for i := x + 1; i < right; i++ {
		c.put(i, top, '_', artFlower)
	}
	c.put(right, top, '/', artFlower)

	bodyLeft, bodyRight := x+1, right-1
	for y := top + 1; y < top+rows-1; y++ {
		c.put(bodyLeft, y, '|', artFlower)
		c.put(bodyRight, y, '|', artFlower)
	}
	base := top + rows - 1
	if base >= top+2 {
		c.put(bodyLeft, base, '\\', artFlower)
		for i := bodyLeft + 1; i < bodyRight; i++ {
			c.put(i, base, '_', artFlower)
		}
		c.put(bodyRight, base, '/', artFlower)
	}
}

// ---------------------------------------------------------------------------
// Interference: six wave families chosen by seed. The field is evaluated from
// the elapsed time only, so the pattern never accumulates history.
// ---------------------------------------------------------------------------

type natInterference struct {
	seed       uint64
	family     int
	n          int
	rate       float64
	baseAngle  float64
	separation float64
}

func newInterferenceScene(seed uint64) artScene {
	rng := artRand(seed, 0x696e7466)
	it := &natInterference{
		seed:       seed,
		family:     rng.IntN(6),
		n:          3 + rng.IntN(3),
		rate:       0.22 + rng.Float64()*0.18,
		baseAngle:  rng.Float64() * math.Pi,
		separation: 9 + rng.Float64()*4,
	}
	return artSceneFunc(it.draw)
}

func natInterferenceCell(v float64) (rune, artTone) {
	switch {
	case v < 0.34:
		return ' ', artMuted
	case v < 0.46:
		return '.', artMuted
	case v < 0.57:
		return ':', artMuted
	case v < 0.68:
		return '-', artWater
	case v < 0.78:
		return '=', artWater
	case v < 0.88:
		return '+', artAccent
	case v < 0.96:
		return '*', artAccent
	default:
		return '#', artFlower
	}
}

func (it *natInterference) draw(c *artCanvas, elapsed time.Duration) {
	if c == nil || c.w <= 0 || c.h <= 0 {
		return
	}
	t := elapsed.Seconds()
	phase := t * it.rate
	w, h := c.w, c.h
	cx := float64(w-1) * 0.5
	cy := float64(h-1) * 0.5

	for y := 0; y < h; y++ {
		py := float64(y) - cy
		for x := 0; x < w; x++ {
			px := (float64(x) - cx) * 2.0
			var v float64
			switch it.family {
			case 0: // two coherent sources, deliberately not mirror-symmetric
				r1 := math.Hypot(px+10.0, py+7.0)
				r2 := math.Hypot(px-8.0, py+3.0)
				v = 0.5 + 0.25*(math.Cos(0.62*r1-phase)+math.Cos(0.62*r2+phase))
			case 1: // moire of two chirped zone plates
				r1 := math.Hypot(px+1.5, py+1.0)
				r2 := math.Hypot(px-2.5, py-1.5)
				v = 0.5 + 0.25*(math.Sin(0.030*r1*r1-phase)+math.Sin(0.030*r2*r2+phase))
			case 2: // five-fold quasicrystal
				sum := 0.0
				for k := 0; k < 5; k++ {
					a := float64(k)*2*math.Pi/5 + phase*0.07
					sum += math.Cos(0.95*(px*math.Cos(a)+py*math.Sin(a)) + phase + float64(k)*1.3)
				}
				v = 0.5 + 0.5*sum/5
			case 3: // twisted vortex beam
				ang := math.Atan2(py, px)
				rad := math.Hypot(px, py)
				v = 0.5 + 0.5*math.Sin(float64(it.n)*ang+0.58*rad-phase*1.8)
			case 4: // three-beam lattice
				sum := 0.0
				rot := 0.18 * math.Sin(phase*0.20)
				for k := 0; k < 3; k++ {
					a := float64(k)*2*math.Pi/3 + rot
					sum += math.Cos(0.68*(px*math.Cos(a)+py*math.Sin(a)) + phase + float64(k)*0.4)
				}
				v = 0.5 + 0.5*sum/3
			default: // Fresnel spiral zone plate
				ang := math.Atan2(py, px)
				rad := math.Hypot(px, py)
				v = 0.5 + 0.5*math.Cos(0.032*rad*rad+float64(it.n)*ang+phase)
			}
			ch, tone := natInterferenceCell(v)
			if ch != ' ' {
				c.put(x, y, ch, tone)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Windmill: a slowly turning windmill in a quiet, dry rural setting.
// Seeded terrain, rotating sails and occasional birds, without message reactions.
// ---------------------------------------------------------------------------

type natWindmill struct {
	seed  uint64
	phase float64
	rate  float64
}

func newWindmillScene(seed uint64) artScene {
	rng := artRand(seed, 0x6c616e64)
	ls := &natWindmill{
		seed: seed, phase: rng.Float64() * 2 * math.Pi,
		rate: 0.35 + rng.Float64()*0.1,
	}
	return artSceneFunc(ls.draw)
}

func (ls *natWindmill) draw(c *artCanvas, elapsed time.Duration) {
	if c == nil || c.w <= 0 || c.h <= 0 {
		return
	}
	cx := c.w / 2
	if c.w < 7 || c.h < 8 {
		ls.drawGround(c, cx)
		return
	}
	base := c.h - 3
	height := min(natClampInt(c.h*2/5, 4, 14), base-1)
	radius := min(5, (c.w-3)/2, 2*(base-height))
	cx = natClampInt(cx+int(ls.seed%3)-1, radius+1, c.w-radius-2)
	ls.drawGround(c, cx)
	angle := ls.phase + max(time.Duration(0), elapsed).Seconds()*ls.rate
	ls.drawWindmill(c, cx, base, height, radius, angle)
	// Leave a clear row above the highest possible sail tip.
	ls.drawBirds(c, elapsed, base-height-int(math.Ceil(float64(radius)*0.5)))
}

func (ls *natWindmill) drawBirds(c *artCanvas, elapsed time.Duration, skyRows int) {
	if skyRows < 3 {
		return
	}
	const cycle = 14 * time.Second
	elapsed = max(time.Duration(0), elapsed)
	pass := uint64(elapsed / cycle)
	hash := func(n uint64) uint64 { return storyHash(ls.seed, pass, n, 44) }
	start := time.Duration(300+hash(0)%1201) * time.Millisecond
	age := elapsed%cycle - start
	if age < 0 {
		return
	}
	duration := time.Duration(7000+hash(1)%801) * time.Millisecond
	bandRows := min(skyRows, 11)
	bandY := int(hash(7) % uint64(skyRows-bandRows+1))
	count := min([...]int{1, 2, 3, 3, 3, 3, 4, 4}[hash(2)%8], (bandRows+1)/2)
	// Keep a loose V-shaped flock, with enough vertical room that individual
	// silhouettes never overwrite one another. Tall panes move the sky band,
	// not the birds' speed or the scale of their maneuvers.
	order := [...]int{0, 1, 2, 3}
	if count == 3 {
		order = [...]int{1, 0, 2, 3}
	} else if count == 4 {
		order = [...]int{2, 1, 3, 0}
	}
	slack := float64(bandRows - 1 - 2*(count-1))
	margin := math.Min(0.35, slack/2)
	altitude := func(n uint64) float64 {
		return margin + float64(hash(n)%1024)/1023*(slack-2*margin)
	}
	a, control, b := altitude(3), altitude(4), altitude(5)
	group := math.Min(1, age.Seconds()/duration.Seconds())
	group = group * group * (3 - 2*group)
	y := (1-group)*(1-group)*a + 2*group*(1-group)*control + group*group*b
	turning := c.w >= 12 && skyRows >= 6 && hash(8)%3 == 0
	for i := 0; i < count; i++ {
		q := storyHash(ls.seed, pass, uint64(i), 45)
		delay := time.Duration(i)*time.Duration(700+q%201)*time.Millisecond +
			time.Duration((q>>10)%201)*time.Millisecond
		flight := duration + time.Duration(int((q>>20)%401)-200)*time.Millisecond
		local := age - delay
		if local < 0 || local >= flight {
			continue
		}
		p := local.Seconds() / flight.Seconds()
		// Speed changes smoothly. Some flocks bank and return through the entry
		// edge instead of all following the same straight crossing.
		x := -2 + float64(c.w+3)*(p+0.10*math.Sin(2*math.Pi*p))
		if turning {
			depth := float64(c.w-5+int(hash(9)%4)) + float64(int((q>>28)%3)-1)*0.25
			x = -2 + depth*math.Sin(math.Pi*p)
		}
		if hash(6)&1 != 0 {
			x = float64(c.w-1) - x
		}
		flutter := margin * 0.75 * math.Sin(local.Seconds()*(1+float64(q%5)*0.1)+float64(q%17))
		row := bandY + int(math.Round(y+float64(2*order[i])+flutter))
		// Independent bursts of wingbeats alternate with sustained gliding;
		// birds flap through a bank instead of changing direction like robots.
		glideCycle := time.Duration(2600+(q>>40)%1401) * time.Millisecond
		burst := time.Duration(800+(q>>48)%701) * time.Millisecond
		gliding := local%glideCycle >= burst && !(turning && p > 0.32 && p < 0.68)
		wings := "-v-"
		if !gliding {
			beat := time.Duration(100+(q>>32)%91) * time.Millisecond
			switch (int(local/beat) + int(q%4)) % 4 {
			case 0:
				wings = "\\v/"
			case 2:
				wings = "/v\\"
			}
		}
		c.text(int(math.Round(x))-1, row, wings, artMuted)
	}
}

func (ls *natWindmill) groundY(x, w, h, cx int) int {
	phase := natUnit(ls.seed, 17, 43) * 2 * math.Pi
	u, anchor := float64(x)/float64(w), float64(cx)/float64(w)
	return natClampInt(h-3+int(math.Round(0.8*(math.Sin(u*4+phase)-math.Sin(anchor*4+phase)))), 0, h-1)
}

func (ls *natWindmill) drawGround(c *artCanvas, cx int) {
	for layer := 0; layer < 2; layer++ {
		previous := 0
		for x := 0; x < c.w; x++ {
			y := ls.groundY(x, c.w, c.h, cx)
			if layer == 0 {
				u := float64(x) / float64(c.w)
				y = natClampInt(c.h-3-max(2, c.h/6)+int(math.Round(1.4*math.Sin(u*5+natUnit(ls.seed, 19, 43)*6.28))), 0, c.h-1)
			}
			ch := '_'
			if x > 0 {
				if y < previous {
					ch = '/'
				} else if y > previous {
					ch = '\\'
				}
				if absArt(y-previous) > 1 {
					c.line(x-1, previous, x, y, ch, artMuted)
				}
			}
			c.put(x, y, ch, artMuted)
			previous = y
		}
	}
	if c.h >= 8 {
		for _, x := range [...]int{1, c.w - 2} {
			if absArt(x-cx) < 4 {
				continue
			}
			y := ls.groundY(x, c.w, c.h, cx)
			c.text(x-1, y-1, "\\|/", artLeaf)
			c.put(x, y, '|', artLeaf)
		}
	}
}

func (ls *natWindmill) drawWindmill(c *artCanvas, cx, base, height, radius int, angle float64) {
	roof := base - height
	half := min(3, (c.w-3)/2, max(1, height/3))
	bodyTop := roof + half + 1
	for y := bodyTop; y <= base; y++ {
		for x := cx - half; x <= cx+half; x++ {
			c.put(x, y, ' ', artMuted)
		}
		c.put(cx-half, y, '|', artMuted)
		c.put(cx+half, y, '|', artMuted)
	}
	c.put(cx, roof, '^', artMuted)
	for row := 1; row <= half; row++ {
		c.put(cx-row, roof+row, '/', artMuted)
		c.put(cx+row, roof+row, '\\', artMuted)
		for x := cx - row + 1; x < cx+row; x++ {
			ch := ' '
			if row == half {
				ch = '_'
			}
			c.put(x, roof+row, ch, artMuted)
		}
	}
	for x := cx - half; x <= cx+half; x++ {
		c.put(x, base, '_', artMuted)
	}
	if half >= 2 {
		c.text(cx-1, max(bodyTop, base-4), "[+]", artWarm)
		c.text(cx-1, base-1, "| |", artMuted)
		c.text(cx-1, base, "|_|", artMuted)
	} else {
		c.put(cx, base-1, '|', artMuted)
	}

	// Correct for terminal-cell aspect ratio. Each sail follows the same
	// continuous angle, with a slender spar and a slatted outer paddle.
	hubY := roof + 1
	for arm := 0; arm < 4; arm++ {
		a := angle + float64(arm)*math.Pi/2
		vx, vy := math.Cos(a), math.Sin(a)
		ch := '-'
		switch {
		case math.Abs(vx) < 0.35:
			ch = '|'
		case math.Abs(vy) >= 0.35:
			ch = '\\'
			if vx*vy < 0 {
				ch = '/'
			}
		}
		px, py := cx, hubY
		for step := 1; step <= radius*4; step++ {
			r := float64(step) / 4
			x := cx + int(math.Round(vx*r))
			y := hubY + int(math.Round(vy*r*0.5))
			glyph := ch
			if r >= float64(radius)*0.6 {
				glyph = '#'
			}
			c.line(px, py, x, y, glyph, artAccent)
			px, py = x, y
		}
	}
	c.put(cx, hubY, 'O', artWarm)
}

// ---------------------------------------------------------------------------
// Rain: drops falling into open water, with surface ripples and swimming fish.
// Each seeded stream ties its splash to the preceding drop's impact.
// ---------------------------------------------------------------------------

type natRainSplash struct {
	x      float64
	phase  float64
	period float64
}

type natRain struct {
	seed     uint64
	splashes [8]natRainSplash
}

func newRainScene(seed uint64) artScene {
	rng := artRand(seed, 0x7261696e)
	r := &natRain{seed: seed}
	for i := range r.splashes {
		r.splashes[i] = natRainSplash{
			x:      (float64(i) + 0.25 + rng.Float64()*0.5) / float64(len(r.splashes)),
			phase:  rng.Float64(),
			period: 3.5 + rng.Float64()*1.5,
		}
	}
	return artSceneFunc(r.draw)
}

func (r *natRain) draw(c *artCanvas, elapsed time.Duration) {
	if c == nil || c.w <= 0 || c.h <= 0 {
		return
	}
	t := max(time.Duration(0), elapsed).Seconds()
	waterTop := max(1, c.h*2/3)
	r.drawPond(c, t, waterTop)
	r.drawRain(c, t, waterTop)
	r.drawFish(c, t, waterTop)
}

func (r *natRain) drawPond(c *artCanvas, t float64, waterTop int) {
	w, h := c.w, c.h
	if waterTop >= h {
		return
	}
	for y := waterTop; y < h; y++ {
		row := y - waterTop
		shift := int(math.Round(t * 0.9))
		for x := 0; x < w; x++ {
			if row == 0 {
				c.put(x, y, '~', artWater)
				continue
			}
			switch (x + row*2 + shift) % 6 {
			case 0:
				c.put(x, y, '~', artWater)
			case 4:
				c.put(x, y, '-', artWater)
			}
		}
	}
}

func (r *natRain) drawFish(c *artCanvas, t float64, waterTop int) {
	if c.w < 4 || c.h-waterTop < 4 {
		return
	}
	for i := 0; i < 2; i++ {
		y := waterTop + 3 + i*3
		if y >= c.h {
			break
		}
		span := max(1, c.w-4)
		phase := natFrac(t*(0.7+float64(i)*0.15)/float64(span*2) + natUnit(r.seed, i, 83))
		x, fish := int(phase*float64(span*2)), "><o>"
		if phase >= 0.5 {
			x, fish = int((1-phase)*float64(span*2)), "<o><"
		}
		c.text(min(x, c.w-4), y, fish, artAccent)
	}
}

// Each stream uses one timeline for the drop, impact and expanding ripple.
// Rain stops at the surface rather than falling through the fish below it.
func (r *natRain) drawRain(c *artCanvas, t float64, waterTop int) {
	for i := range r.splashes {
		sp := r.splashes[i]
		age := natFrac(t/sp.period+sp.phase) * sp.period
		x := natClampInt(int(sp.x*float64(c.w)), 0, c.w-1)
		fall := sp.period * 0.65
		if age < fall {
			y := int(age / fall * float64(waterTop))
			c.put(x, y, '|', artWater)
			c.put(x, y-1, '\'', artMuted)
			continue
		}
		age -= fall
		if age < 0.45 {
			// Detached beads rise and fall on unequal arcs, not a solid cone.
			spread, rise := 1+int(age*5), 1
			if age >= 0.10 && age < 0.28 {
				rise = 2
			}
			c.put(x-spread, waterTop-rise, '\'', artWater)
			c.put(x+spread, waterTop-1, '.', artWater)
		}
		if age < 1.2 {
			rad := int(age*3.0) + 1
			c.put(x-rad, waterTop, '(', artWater)
			c.put(x+rad, waterTop, ')', artWater)
			if waterTop+1 < c.h {
				c.put(x-rad+1, waterTop+1, ',', artWater)
				c.put(x+rad-1, waterTop+1, '.', artWater)
			}
		}
	}
}

// Story scenes are self-contained ASCII vignettes. They only touch the
// clipped canvas, keep all mutable state in their own receiver, and derive
// their animation from elapsed time so a paused pane does no extra work.

const (
	storySnakeTick   = 300 * time.Millisecond
	storySnakeMaxLen = 12
	storySnakeMinLen = 4
)

func newStoryScene(name string, seed uint64) artScene {
	switch name {
	case "skyscraper":
		return &skyscraperScene{seed: seed}
	case "bob":
		return &bobScene{seed: seed}
	case "tank":
		return &tankScene{seed: seed}
	case "snake":
		return &snakeScene{seed: seed}
	case "maze":
		return &mazeScene{seed: seed}
	case "spacewars":
		return &spacewarsScene{seed: seed}
	}
	return nil
}

// storyHash is a small splitmix-style mixer. It keeps layouts stable without
// allocating a random generator in the drawing loop.
func storyHash(seed uint64, a, b, c uint64) uint64 {
	x := seed ^ a*0x9e3779b97f4a7c15 ^ b*0xc2b2ae3d27d4eb4f ^ c*0x165667b19e3779f9
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

// Spacewars is a distant, fixed-camera skirmish, not a cockpit or scrolling sky.
type spacewarsScene struct{ seed uint64 }

type spacewarsBattle struct {
	seed                        uint64
	w, h                        int
	appear, death               [2]time.Duration
	anchor                      [2]storyPoint
	enter, engage, leave, pulse time.Duration
}

func (s *spacewarsScene) battle(w, h int, id uint64) spacewarsBattle {
	b := spacewarsBattle{seed: storyHash(s.seed, id, 0, 51), w: w, h: h}
	hash := func(n uint64) uint64 { return storyHash(b.seed, n, 0, 52) }
	for i := range b.appear {
		b.appear[i] = time.Duration(200+hash(uint64(i))%1401) * time.Millisecond
		b.anchor[i] = storyPoint{3 + int(hash(uint64(i)+20)%uint64(max(1, w/3-2))),
			h*2/3 + int(hash(uint64(i)+30)%uint64(max(1, h/8)))}
		if i == 1 {
			b.anchor[i].x = w - 1 - b.anchor[i].x
			b.anchor[i].y = h - 1 - b.anchor[i].y
		}
	}
	b.enter = time.Duration(1400+hash(4)%1001) * time.Millisecond
	b.engage = max(b.appear[0], b.appear[1]) + b.enter + 400*time.Millisecond
	first := b.engage + time.Duration(1400+hash(5)%801)*time.Millisecond
	outcome := int(hash(6) % 3)
	victim := outcome
	if outcome == 2 {
		victim = int(hash(8) % 2)
	}
	b.death[victim] = first
	if outcome == 2 {
		// The return shot is already in flight when its shooter is destroyed.
		b.death[1-victim] = first + time.Duration(250+hash(9)%251)*time.Millisecond
	}
	b.leave = first + time.Second
	b.pulse = time.Duration(800+hash(10)%201) * time.Millisecond
	return b
}

func spacewarsMix(a, b storyPoint, fraction float64) storyPoint {
	fraction = math.Max(0, math.Min(1, fraction))
	return storyPoint{a.x + int(math.Round(float64(b.x-a.x)*fraction)),
		a.y + int(math.Round(float64(b.y-a.y)*fraction))}
}

func (b spacewarsBattle) position(team int, at time.Duration) storyPoint {
	if b.death[team] > 0 {
		at = min(at, b.death[team])
	}
	start, end := storyPoint{-2, b.h + 1}, storyPoint{b.w + 2, -2}
	if team == 1 {
		start, end = storyPoint{b.w + 1, -2}, storyPoint{-3, b.h + 2}
	}
	arrival := b.appear[team] + b.enter
	if at < arrival {
		return spacewarsMix(start, b.anchor[team], float64(at-b.appear[team])/float64(b.enter))
	}
	t := (min(at, b.leave) - arrival).Seconds()
	q := storyHash(b.seed, uint64(team), 0, 53)
	p := b.anchor[team]
	p.x += int(math.Round(0.8 * math.Sin(t*(0.8+float64(q%7)*0.04))))
	p.y += int(math.Round(0.8 * math.Sin(t*(1.0+float64((q>>8)%7)*0.05))))
	if at > b.leave {
		return spacewarsMix(p, end, float64(at-b.leave)/float64(2200*time.Millisecond))
	}
	return p
}

func (b spacewarsBattle) drawShot(c *artCanvas, team int, launch, impact, at time.Duration, miss bool) {
	if at < launch || at >= impact || launch < b.engage ||
		(b.death[team] > 0 && launch >= b.death[team]) ||
		(b.death[1-team] > 0 && launch >= b.death[1-team]) {
		return
	}
	direction := 1 - 2*team
	from, to := b.position(team, launch), b.position(1-team, impact)
	from.x, from.y = from.x+2*direction, from.y-direction
	if miss {
		to.x, to.y = to.x+3*direction, to.y+2*direction
	}
	fraction := float64(at-launch) / float64(impact-launch)
	trail := spacewarsMix(from, to, fraction-0.18)
	p := spacewarsMix(from, to, fraction)
	c.line(trail.x, trail.y, p.x, p.y, '/', artWarm)
}

func (s *spacewarsScene) draw(c *artCanvas, elapsed time.Duration) {
	if c == nil || c.w <= 0 || c.h <= 0 {
		return
	}
	elapsed = max(time.Duration(0), elapsed)
	for i := 0; i < min(18, 2+c.w*c.h/80); i++ {
		x := int(storyHash(s.seed, uint64(i), 0, 54) % uint64(c.w))
		y := int(storyHash(s.seed, uint64(i), 1, 54) % uint64(c.h))
		ch := '.'
		if (int(elapsed/(2*time.Second))+i)%7 == 0 {
			ch = '+'
		}
		c.put(x, y, ch, artMuted)
	}
	if c.w < 9 || c.h < 12 {
		return
	}
	const cycle = 12 * time.Second
	b, at := s.battle(c.w, c.h, uint64(elapsed/cycle)), elapsed%cycle
	for team := 0; team < 2; team++ {
		// Harassing volleys miss; lethal pulses intercept the moving target at
		// its planned impact position. No new shot launches from a wreck.
		for shot := 0; shot < 4; shot++ {
			q := storyHash(b.seed, uint64(team), uint64(shot), 55)
			launch := b.engage + time.Duration(shot)*time.Duration(700+q%351)*time.Millisecond +
				time.Duration((q>>12)%301)*time.Millisecond
			b.drawShot(c, team, launch, launch+b.pulse, at, true)
		}
		if hit := b.death[1-team]; hit > 0 {
			b.drawShot(c, team, hit-b.pulse, hit, at, false)
		}
	}
	for team := 0; team < 2; team++ {
		if at < b.appear[team] || (b.death[team] > 0 && at >= b.death[team]) || at >= b.leave+2200*time.Millisecond {
			continue
		}
		p := b.position(team, at)
		// Readable front-view fighters: connected wings, fuselage and cockpit.
		sprite, tone := [...]string{"\\   /", "-[O]-", "/   \\"}, artAccent
		if team == 1 {
			sprite, tone = [...]string{"|   |", "|=O=|", "|   |"}, artFlower
		}
		for row, text := range sprite {
			// Opaque spaces keep stars and passing shots out of the silhouette.
			c.text(p.x-2, p.y-1+row, text, tone)
		}
	}
	for team, hit := range b.death {
		if hit > 0 && at >= hit && at < hit+800*time.Millisecond {
			p := b.position(team, hit)
			drawSpacewarsBlast(c, p.x, p.y, at-hit)
		}
	}
}

func drawSpacewarsBlast(c *artCanvas, x, y int, age time.Duration) {
	if age < 125*time.Millisecond {
		c.put(x, y, '*', artWarm)
		return
	}
	if age < 300*time.Millisecond {
		c.put(x, y, '#', artWarm)
		for _, d := range [...]storyPoint{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
			c.put(x+d.x, y+d.y, '*', artDanger)
		}
		return
	}
	for _, d := range [...]storyPoint{{-1, -1}, {1, -1}, {-1, 1}, {1, 1}} {
		ch, tone, spread := '*', artWarm, 1
		if age >= 500*time.Millisecond {
			ch, tone, spread = '.', artDanger, 2
		}
		c.put(x+d.x*spread, y+d.y, ch, tone)
	}
}

// Maze builds a perfect maze, holds it, then sheds its walls as falling grains.
// Only the current cycle is generated, even after a long pause or time jump.
type mazeScene struct {
	seed, epoch uint64
	w, h        int
	walls       []bool
}

func (s *mazeScene) generate(w, h int, epoch uint64) {
	s.w, s.h, s.epoch = w, h, epoch
	if cap(s.walls) < w*h {
		s.walls = make([]bool, w*h)
	} else {
		s.walls = s.walls[:w*h]
		clear(s.walls)
	}
	// Two-column corridors compensate for the terminal's tall character cells.
	cols, rows := (w-1)/3, (h-1)/2
	mw, mh := cols*3+1, rows*2+1
	ox, oy := (w-mw)/2, (h-mh)/2
	for y := 0; y < mh; y++ {
		for x := 0; x < mw; x++ {
			if y%2 == 0 || x%3 == 0 {
				s.walls[(oy+y)*w+ox+x] = true
			}
		}
	}
	rng := artRand(s.seed, storyHash(epoch, uint64(w), uint64(h), 0x6d617a65))
	visited := make([]bool, cols*rows)
	stack := make([]int, 1, len(visited))
	stack[0] = rng.IntN(len(visited))
	visited[stack[0]] = true
	for len(stack) > 0 {
		cell := stack[len(stack)-1]
		x, y := cell%cols, cell/cols
		var choices [4]int
		n := 0
		for _, d := range [...]storyPoint{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
			nx, ny := x+d.x, y+d.y
			if nx >= 0 && nx < cols && ny >= 0 && ny < rows && !visited[ny*cols+nx] {
				choices[n] = ny*cols + nx
				n++
			}
		}
		if n == 0 {
			stack = stack[:len(stack)-1]
			continue
		}
		next := choices[rng.IntN(n)]
		nx, ny := next%cols, next/cols
		if ny == y {
			s.walls[(oy+y*2+1)*w+ox+max(x, nx)*3] = false
		} else {
			for dx := 1; dx <= 2; dx++ {
				s.walls[(oy+max(y, ny)*2)*w+ox+x*3+dx] = false
			}
		}
		visited[next] = true
		stack = append(stack, next)
	}
	// Open an entrance and exit at opposite ends of the connected passages.
	s.walls[(oy+1)*w+ox] = false
	s.walls[(oy+mh-2)*w+ox+mw-1] = false
}

func (s *mazeScene) draw(c *artCanvas, elapsed time.Duration) {
	if c == nil || c.w < 4 || c.h < 3 {
		return
	}
	const build, crumble, empty, cycle = 2 * time.Second, 7 * time.Second, 10 * time.Second, 11 * time.Second
	elapsed = max(time.Duration(0), elapsed)
	epoch, age := uint64(elapsed/cycle), elapsed%cycle
	if age >= empty {
		return
	}
	if s.w != c.w || s.h != c.h || s.epoch != epoch {
		s.generate(c.w, c.h, epoch)
	}
	for i, wall := range s.walls {
		if !wall {
			continue
		}
		x, y := i%c.w, i/c.w
		q := storyHash(s.seed, epoch, uint64(i), 0x6d617a65)
		born := time.Duration(y)*1500*time.Millisecond/time.Duration(c.h) + time.Duration(q%400)*time.Millisecond
		if age < build && age < born {
			continue
		}
		delay := time.Duration((q>>8)%900) * time.Millisecond
		if age >= crumble+delay {
			continue
		}
		horizontal := (x > 0 && s.walls[i-1]) || (x+1 < c.w && s.walls[i+1])
		vertical := (y > 0 && s.walls[i-c.w]) || (y+1 < c.h && s.walls[i+c.w])
		ch := '+'
		if horizontal && !vertical {
			ch = '-'
		} else if vertical && !horizontal {
			ch = '|'
		}
		tone := artMuted
		if age < build && age-born < 250*time.Millisecond {
			tone = artAccent
		}
		c.put(x, y, ch, tone)
	}
	if age < crumble {
		return
	}
	// Paint debris after intact walls; every grain starts at its own wall cell.
	for i, wall := range s.walls {
		if !wall {
			continue
		}
		q := storyHash(s.seed, epoch, uint64(i), 0x6d617a65)
		fall := age - crumble - time.Duration((q>>8)%900)*time.Millisecond
		if fall < 0 {
			continue
		}
		t := fall.Seconds()
		vx := float64(int((q>>20)%7)-3) * 0.8
		x := i%c.w + int(math.Round(vx*t))
		y := i/c.w + int(t*t*float64(c.h)*0.65)
		ch, tone := '.', artMuted
		if fall < 300*time.Millisecond {
			ch, tone = ':', artAccent
		}
		c.put(x, y, ch, tone)
	}
}

type skyscraperScene struct {
	seed uint64
}

// skyscraper draws a small night skyline: four towers with different crowns,
// slowly changing lit windows, drifting clouds and a blinking beacon.
func (s *skyscraperScene) draw(c *artCanvas, elapsed time.Duration) {
	if c.w <= 0 || c.h <= 0 {
		return
	}
	if elapsed < 0 {
		elapsed = 0
	}
	w, h := c.w, c.h
	if h < 3 || w < 3 {
		return
	}
	ground := h - 1

	type tower struct {
		x0, x1, roof, style int
	}
	specs := [...]struct {
		from, to, pct, style int
	}{
		{0, 4, 56, 0},
		{4, 7, 80, 1},
		{7, 12, 46, 2},
		{12, 16, 64, 3},
	}
	var towers [4]tower
	n := 0
	minRoof := h
	for _, sp := range specs {
		x0, x1 := w*sp.from/16, w*sp.to/16
		if x1-x0 < 2 {
			continue
		}
		roof := ground - h*sp.pct/100
		if roof > ground-3 {
			roof = ground - 3
		}
		if roof < 1 {
			roof = 1
		}
		if roof >= ground {
			continue
		}
		towers[n] = tower{x0: x0, x1: x1, roof: roof, style: sp.style}
		n++
		if roof < minRoof {
			minRoof = roof
		}
	}
	if n == 0 {
		return
	}

	// Stars stay above the skyline so no building has stars inside it.
	starCount := 3 + w*h/90
	if starCount > 28 {
		starCount = 28
	}
	for i := 0; i < starCount; i++ {
		x := int(storyHash(s.seed, 2, uint64(i), uint64(w)) % uint64(w))
		y := int(storyHash(s.seed, 3, uint64(i), uint64(h)) % uint64(minRoof))
		phase := int(elapsed/(900*time.Millisecond)) + i
		switch {
		case phase%7 == 0:
			c.put(x, y, '*', artAccent)
		case phase%3 == 0:
			c.put(x, y, '.', artMuted)
		}
	}

	// A simple full moon, high enough that the towers never cover it.
	if w >= 10 && minRoof >= 3 {
		mx := w - 5
		c.put(mx+1, 0, '_', artWarm)
		c.put(mx+2, 0, '_', artWarm)
		c.put(mx, 1, '(', artWarm)
		c.put(mx+3, 1, ')', artWarm)
		c.put(mx+1, 2, '-', artWarm)
		c.put(mx+2, 2, '-', artWarm)
	}

	// Generate each passing cloud's size and lobed contour, not a stock sprite.
	// The opaque silhouette passes in front of both stars and the moon.
	if w >= 6 && minRoof >= 3 {
		const cloudWidth = 14
		travel := int64(w + cloudWidth + 2)
		count := 1
		if minRoof >= 6 {
			count = 2
		}
		for i := 0; i < count; i++ {
			period := time.Duration(900+storyHash(s.seed, uint64(i), 0, 24)%501) * time.Millisecond
			phase := int64(elapsed/period) + int64(storyHash(s.seed, uint64(i), 1, 24)%uint64(travel))
			pass, step := uint64(phase/travel), int(phase%travel)
			x := step - cloudWidth
			if storyHash(s.seed, uint64(i), 2, 24)&1 != 0 {
				x = w - 1 - step
			}
			cloudSeed := storyHash(s.seed, uint64(i), pass, 26)
			width := min(w, 8+int(storyHash(cloudSeed, 0, 0, 27)%7))
			height := min(minRoof, 3+int(storyHash(cloudSeed, 0, 1, 27)%2))
			y := int(storyHash(s.seed, uint64(i), pass, 25) % uint64(minRoof-height+1))
			drawStoryCloud(c, x, y, width, height, cloudSeed)
		}
	}

	epoch := int(elapsed / (2600 * time.Millisecond))
	blink := int(elapsed / (900 * time.Millisecond))
	for i := 0; i < n; i++ {
		t := towers[i]
		width := t.x1 - t.x0

		// Body and roofline.
		for y := t.roof + 1; y < ground; y++ {
			c.put(t.x0, y, '|', artMuted)
			c.put(t.x1-1, y, '|', artMuted)
		}
		for x := t.x0; x < t.x1; x++ {
			c.put(x, t.roof, '_', artMuted)
		}

		// Windows: each window follows its own slow cycle, so only a few
		// lights change every couple of seconds.
		if width >= 3 {
			rowStep := 2
			if t.style == 2 {
				rowStep = 3
			}
			step := 2
			if width <= 4 {
				step = 1
			}
			litRate := uint64(4 + t.style%3)
			for y := t.roof + 2; y <= ground-2; y += rowStep {
				for x := t.x0 + 1; x <= t.x1-2; x += step {
					q := storyHash(s.seed, uint64(i), uint64(y*w+x), 0)
					state := (q%11 + uint64(epoch)) % 11
					if state < litRate {
						c.put(x, y, 'o', artWarm)
					} else if q%4 == 0 {
						c.put(x, y, '.', artMuted)
					}
				}
			}
		}

		xc := (t.x0 + t.x1) / 2
		switch t.style {
		case 0:
			// Flat roof with a mast and a blinking aircraft light.
			if t.roof >= 3 {
				c.put(xc, t.roof-1, '|', artMuted)
				if (blink+i)%2 == 0 {
					c.put(xc, t.roof-2, '*', artDanger)
				}
			}
		case 1:
			// Stepped crown with a small spire.
			if width >= 4 && t.roof >= 5 {
				px0, px1 := t.x0+1, t.x1-2
				for x := px0; x <= px1; x++ {
					c.put(x, t.roof-2, '_', artMuted)
				}
				c.put(px0, t.roof-1, '|', artMuted)
				c.put(px1, t.roof-1, '|', artMuted)
				c.put(xc, t.roof-3, '^', artAccent)
				if (blink+i)%3 == 0 {
					c.put(xc, t.roof-4, '*', artDanger)
				}
			} else if t.roof >= 3 {
				c.put(xc, t.roof-1, '^', artAccent)
			}
		case 2:
			// Rooftop water tank on a stubby frame.
			if width >= 4 && t.roof >= 3 {
				c.put(xc-1, t.roof-2, '_', artMuted)
				c.put(xc, t.roof-2, '_', artMuted)
				c.put(xc+1, t.roof-2, '_', artMuted)
				c.put(xc-1, t.roof-1, '|', artMuted)
				c.put(xc, t.roof-1, '=', artWater)
				c.put(xc+1, t.roof-1, '|', artMuted)
			}
		case 3:
			// A little rooftop garden.
			if t.roof >= 2 {
				for x := t.x0 + 1; x < t.x1-1; x++ {
					q := storyHash(s.seed, uint64(x), uint64(t.roof), 9)
					ch, tone := '&', artLeaf
					switch {
					case q%17 == 0:
						ch, tone = 'o', artFlower
					case q%3 == 0:
						ch = '*'
					}
					c.put(x, t.roof-1, ch, tone)
				}
			}
		}

		// A warm lobby light at street level.
		if width >= 3 && ground-1 > t.roof+1 {
			c.put(xc, ground-1, 'o', artWarm)
		}
	}

	for x := 0; x < w; x++ {
		c.put(x, ground, '_', artMuted)
	}
}

func drawStoryCloud(c *artCanvas, x, y, width, height int, seed uint64) {
	var tops [artWidth]int
	lobes := 2 + int(storyHash(seed, 0, 2, 27)%3)
	for dx := 0; dx < width; dx++ {
		tops[dx] = height - 1
		for lobe := 0; lobe < lobes; lobe++ {
			q := storyHash(seed, uint64(lobe), 3, 27)
			center := (float64(lobe) + 0.3 + float64(q%41)/100) * float64(width-1) / float64(lobes)
			radius := float64(width) / float64(lobes) * (0.50 + float64((q>>8)%16)/100)
			distance := math.Abs(float64(dx)-center) / radius
			if distance < 1 {
				rise := float64(height-1) * (0.85 + float64((q>>16)%16)/100)
				top := height - 1 - int(math.Round(rise*math.Sqrt(1-distance*distance)))
				tops[dx] = min(tops[dx], top)
			}
		}
	}
	for dx := 0; dx < width; dx++ {
		for dy := tops[dx]; dy < height; dy++ {
			ch := ' '
			switch {
			case dy == height-1:
				ch = '-'
				if dx == 0 {
					ch = '`'
				} else if dx == width-1 {
					ch = '\''
				}
			case dy == tops[dx]:
				ch = '_'
				if dx > 0 && tops[dx-1] < dy {
					ch = '\\'
				} else if dx+1 < width && tops[dx+1] < dy {
					ch = '/'
				}
			case dx == 0:
				ch = '/'
			case dx == width-1:
				ch = '\\'
			}
			// Spaces inside the cloud erase background glyphs too.
			c.put(x+dx, y+dy, ch, artMuted)
		}
	}
}

type bobScene struct {
	seed uint64
}

// Keep Bob in view while an endless trunk and its branches scroll down.
// There are no roots, treetop, rest phase, or jump back to the starting point.
func (s *bobScene) draw(c *artCanvas, elapsed time.Duration) {
	if c.w <= 0 || c.h <= 0 {
		return
	}
	if elapsed < 0 {
		elapsed = 0
	}
	w, h := c.w, c.h
	if w < 5 || h < 8 {
		return
	}
	cx := w / 2
	trunkW := 1
	if w >= 10 {
		trunkW = 2
	}
	tx0 := cx - trunkW/2
	if tx0 < 1 {
		tx0 = 1
	}
	if tx0+trunkW > w-1 {
		tx0 = w - 1 - trunkW
	}
	if tx0 < 0 {
		tx0 = 0
	}

	climbed := int64(elapsed / (650 * time.Millisecond))
	for y := 0; y < h; y++ {
		worldY := int64(y) - climbed
		for x := tx0; x < tx0+trunkW; x++ {
			q := storyHash(s.seed, uint64(x), uint64(worldY), 4)
			ch := '#'
			switch {
			case q%17 == 0:
				ch = '='
			case q%11 == 0:
				ch = '|'
			}
			c.put(x, y, ch, artMuted)
		}
	}
	// Include off-screen branch bases so their foliage enters/leaves naturally.
	for y := -2; y < h+4; y++ {
		worldY := int64(y) - climbed
		if worldY%7 != 0 {
			continue
		}
		tipX, startX, slash := tx0-3, tx0-1, '\\'
		if (worldY/7)&1 != 0 {
			tipX, startX, slash = tx0+trunkW+2, tx0+trunkW, '/'
		}
		c.line(startX, y, tipX, y-2, slash, artMuted)
		drawStorySprig(c, tipX, y-2, s.seed, uint64(worldY/7))
	}
	// Climb on either side, then move cell by cell across the front of the
	// trunk. Side changes never reset the world's uninterrupted upward travel.
	left, right := tx0-1, tx0+trunkW
	hold := time.Duration(6+storyHash(s.seed, 0, 0, 12)%5) * time.Second
	cross := time.Duration(right-left) * 450 * time.Millisecond
	leg := hold + cross
	fromRight := (uint64(elapsed/leg)+storyHash(s.seed, 0, 0, 13))&1 != 0
	step := int(max(time.Duration(0), elapsed%leg-hold) / (450 * time.Millisecond))
	headX := left + step
	if fromRight {
		headX = right - step
	}
	drawStoryBob(c, headX-2, max(1, (h-5)/2), int(elapsed/(380*time.Millisecond))%4, headX*2 > left+right)
}

func drawStorySprig(c *artCanvas, x, y int, seed, salt uint64) {
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			q := storyHash(seed, uint64(dx+1), uint64(dy+1), salt)
			if dx != 0 || dy != 0 {
				if q%3 == 0 {
					continue
				}
			}
			ch := '&'
			if q%2 == 0 {
				ch = '*'
			}
			c.put(x+dx, y+dy, ch, artLeaf)
		}
	}
}

func drawStoryBob(c *artCanvas, x, y, pose int, mirrored bool) {
	put := func(dx, dy int, ch rune) {
		if mirrored {
			dx = 4 - dx
			if ch == '/' {
				ch = '\\'
			} else if ch == '\\' {
				ch = '/'
			}
		}
		c.put(x+dx, y+dy, ch, artAccent)
	}
	put(2, 0, 'o')
	put(2, 1, '|')
	put(2, 2, '|')
	switch pose % 4 {
	case 0:
		put(1, 0, '/')
		put(3, 2, '\\')
		put(1, 3, '/')
		put(3, 4, '\\')
	case 1:
		put(0, 1, '/')
		put(3, 0, '\\')
		put(1, 4, '/')
		put(3, 3, '\\')
	case 2:
		put(0, 1, '/')
		put(4, 1, '\\')
		put(1, 3, '/')
		put(3, 3, '\\')
	default:
		put(1, 0, '/')
		put(3, 0, '\\')
		put(1, 4, '/')
		put(3, 4, '\\')
	}
}

type tankScene struct {
	seed uint64
}

// tank drives up a scrolling road, reacting to procedurally placed drones.
// A shell and its target share the same collision time, so the drone disappears
// exactly when the explosion starts.
func (s *tankScene) draw(c *artCanvas, elapsed time.Duration) {
	if c.w <= 0 || c.h <= 0 {
		return
	}
	if elapsed < 0 {
		elapsed = 0
	}
	w, h := c.w, c.h
	if w < 4 || h < 5 {
		return
	}
	cx := w / 2
	tankTop := h - 7
	if tankTop < 0 {
		tankTop = 0
	}
	if tankTop+5 >= h {
		tankTop = max(0, h-6)
	}
	if tankTop < 0 {
		tankTop = 0
	}

	// A common multiple keeps the scrolling world's modulo patterns positive
	// and seamless without accumulating a growing map of scenery.
	const roadPeriod = 3 * 7 * 4 * 9 * 11 * 2 * 6 * 5
	scroll := int(elapsed/(250*time.Millisecond)) + int(s.seed%7)
	for y := 0; y < h; y++ {
		// The tank faces up: roadside landmarks move down the viewport.
		wy := y + roadPeriod - scroll%roadPeriod
		if wy%3 == 0 {
			c.put(0, y, '"', artLeaf)
		} else if wy%7 == 0 {
			c.put(0, y, '.', artMuted)
		}
		if wy%4 == 1 {
			c.put(w-1, y, '"', artLeaf)
		} else if wy%9 == 4 {
			c.put(w-1, y, '.', artMuted)
		}
		if wy%11 == 0 {
			c.put(0, y, 'o', artMuted)
			c.put(w-1, y, 'o', artMuted)
		}
		if wy%2 == 0 {
			c.put(1, y, '|', artMuted)
			c.put(w-2, y, '|', artMuted)
		}
		if wy%6 == 0 {
			c.put(cx, y, '-', artMuted)
		}
		if wy%5 == 3 {
			c.put(3, y, '.', artMuted)
			c.put(w-4, y, '.', artMuted)
		}
	}

	off := 2
	if w >= 12 {
		off = 3 + int(s.seed%2)
	}
	if off > cx-1 {
		off = cx - 1
	}
	if off < 1 {
		off = 1
	}
	leftLane := max(1, cx-off)
	rightLane := min(w-2, cx+off)
	hitY := tankTop / 2
	if hitY < 1 {
		hitY = 1
	}
	if hitY >= tankTop {
		hitY = tankTop - 1
	}
	if hitY < 1 {
		hitY = 1
	}
	if hitY < 2 {
		// Too little vertical room for a target: keep the road and tank.
		drawStoryTank(c, cx, tankTop, 0)
		return
	}

	type contact struct {
		appear, launch, hit time.Duration
		lane                int
	}
	const cycle = 8 * time.Second
	var contacts [2]contact
	for i := range contacts {
		// Derive each attack independently, not just once per scene selection.
		// The schedule stays stable while that target is moving and being shot.
		id := uint64(elapsed/cycle)*2 + uint64(i)
		appear := time.Duration(i)*4*time.Second + time.Duration(150+storyHash(s.seed, id, 0, 21)%501)*time.Millisecond
		launch := appear + time.Duration(800+storyHash(s.seed, id, 1, 21)%301)*time.Millisecond
		lane := [...]int{leftLane, cx, rightLane}[storyHash(s.seed, id, 2, 21)%3]
		contacts[i] = contact{appear: appear, launch: launch, hit: launch + 1100*time.Millisecond, lane: lane}
	}
	t := elapsed % cycle

	aim := 0
	for _, e := range contacts {
		if t >= e.appear+250*time.Millisecond && t <= e.hit+300*time.Millisecond {
			if e.lane < cx {
				aim = -1
			} else if e.lane > cx {
				aim = 1
			}
			break
		}
	}

	// Incoming drones and their explosions.
	if hitY >= 2 {
		for _, e := range contacts {
			switch {
			case t >= e.appear && t < e.hit:
				frac := float64(t-e.appear) / float64(e.hit-e.appear)
				y := 1 + int(float64(hitY-1)*frac)
				drawStoryEnemy(c, e.lane, y)
			case t >= e.hit && t < e.hit+900*time.Millisecond:
				drawStoryBlast(c, e.lane, hitY, t-e.hit)
			}
		}
	}

	// Shells travel from the muzzle to the drone's meeting point.
	for _, e := range contacts {
		if t < e.launch || t >= e.hit {
			continue
		}
		frac := float64(t-e.launch) / float64(e.hit-e.launch)
		startX := cx
		if e.lane < cx {
			startX = cx - 2
		} else if e.lane > cx {
			startX = cx + 2
		}
		x := startX + int(float64(e.lane-startX)*frac)
		y := tankTop + int(float64(hitY-tankTop)*frac)
		ch := '|'
		switch {
		case e.lane < cx:
			ch = '\\'
		case e.lane > cx:
			ch = '/'
		}
		c.put(x, y, ch, artWarm)
		trail := frac - 0.12
		if trail > 0 {
			tx := startX + int(float64(e.lane-startX)*trail)
			ty := tankTop + int(float64(hitY-tankTop)*trail)
			c.put(tx, ty, '.', artMuted)
		}
	}

	drawStoryTank(c, cx, tankTop, aim)
	for _, e := range contacts {
		if t >= e.launch && t <= e.launch+180*time.Millisecond {
			mx := cx
			if e.lane < cx {
				mx = cx - 2
			} else if e.lane > cx {
				mx = cx + 2
			}
			c.put(mx, tankTop-1, '*', artWarm)
		}
	}
}

func drawStoryTank(c *artCanvas, cx, top, aim int) {
	// Top-down silhouette: two long caterpillar tracks surround an armored
	// hull and a separate round turret. Keep the muzzle coordinates unchanged.
	for row, text := range [...]string{".-.___.-.", "|#|[O]|#|", "|#|___|#|", "|#|===|#|", "'-'---'-'"} {
		c.text(cx-4, top+1+row, text, artMuted)
	}
	c.text(cx-1, top+2, "[O]", artAccent)
	switch {
	case aim < 0:
		c.put(cx-2, top, '\\', artAccent)
		c.put(cx-1, top+1, '\\', artAccent)
	case aim > 0:
		c.put(cx+2, top, '/', artAccent)
		c.put(cx+1, top+1, '/', artAccent)
	default:
		c.put(cx, top, '|', artAccent)
		c.put(cx, top+1, '|', artAccent)
	}
}

func drawStoryEnemy(c *artCanvas, x, y int) {
	c.put(x-1, y, '<', artDanger)
	c.put(x, y, 'o', artDanger)
	c.put(x+1, y, '>', artDanger)
	c.put(x, y+1, 'v', artDanger)
}

func drawStoryBlast(c *artCanvas, x, y int, age time.Duration) {
	switch {
	case age < 120*time.Millisecond:
		c.put(x, y, '*', artWarm)
	case age < 260*time.Millisecond:
		c.put(x, y, '*', artWarm)
		c.put(x-1, y, '-', artDanger)
		c.put(x+1, y, '-', artDanger)
		c.put(x, y-1, '|', artDanger)
		c.put(x, y+1, '|', artDanger)
	case age < 420*time.Millisecond:
		c.put(x, y, 'o', artWarm)
		c.put(x-1, y-1, '\\', artDanger)
		c.put(x+1, y-1, '/', artDanger)
		c.put(x-1, y+1, '/', artDanger)
		c.put(x+1, y+1, '\\', artDanger)
		c.put(x-1, y, '-', artWarm)
		c.put(x+1, y, '-', artWarm)
	case age < 620*time.Millisecond:
		c.put(x-1, y-1, '.', artDanger)
		c.put(x+1, y-1, '.', artDanger)
		c.put(x-1, y+1, '.', artDanger)
		c.put(x+1, y+1, '.', artDanger)
		c.put(x, y-2, ':', artMuted)
	}
}

type storyPoint struct {
	x, y int
}

type storyApple struct {
	x, y int
}

type snakeScene struct {
	seed       uint64
	w, h       int
	ready      bool
	last       time.Duration
	pending    time.Duration
	head       storyPoint
	body       []storyPoint
	length     int
	apples     []storyApple
	spawnCount int
	biteX      int
	biteY      int
	biteUntil  time.Duration
	waiting    bool
	waitUntil  time.Duration
}

func (s *snakeScene) draw(c *artCanvas, elapsed time.Duration) {
	if c.w < 3 || c.h < 4 {
		s.ready = false
		return
	}
	if elapsed < 0 {
		elapsed = 0
	}
	if !s.ready || s.w != c.w || s.h != c.h {
		s.reset(c.w, c.h, elapsed)
		s.drawScene(c, elapsed)
		return
	}
	if elapsed < s.last {
		s.last = elapsed
		s.pending = 0
		s.drawScene(c, elapsed)
		return
	}
	delta := elapsed - s.last
	s.last = elapsed

	if s.waiting {
		if elapsed >= s.waitUntil {
			s.reset(c.w, c.h, elapsed)
		}
		s.drawScene(c, elapsed)
		return
	}

	s.pending += delta
	if s.pending > 12*storySnakeTick {
		s.pending = 12 * storySnakeTick
	}
	steps := int(s.pending / storySnakeTick)
	s.pending -= time.Duration(steps) * storySnakeTick
	for i := 0; i < steps; i++ {
		s.step(c.w, c.h, elapsed)
		if s.waiting {
			break
		}
	}
	s.drawScene(c, elapsed)
}

func (s *snakeScene) reset(w, h int, now time.Duration) {
	s.w, s.h = w, h
	s.ready = true
	s.last = now
	s.pending = 0
	s.waiting = false
	s.waitUntil = 0
	s.biteUntil = 0
	s.length = storySnakeMinLen
	x := w / 2
	if x < 1 {
		x = 1
	}
	if x > w-2 {
		x = w - 2
	}
	// Start the entire body below the viewport; clipping reveals the head
	// first, then one following segment per step, including after a restart.
	y := h
	s.head = storyPoint{x: x, y: y}
	s.body = s.body[:0]
	for i := 0; i < s.length; i++ {
		s.body = append(s.body, storyPoint{x: x, y: y + i})
	}
	s.apples = s.apples[:0]
	s.spawnCount++
	s.spawnApple(w, h)
}

func (s *snakeScene) spawnApple(w, h int) {
	s.spawnCount++
	riseMax := s.head.y - 1
	if riseMax < 3 {
		return
	}
	span := riseMax - 2
	if span < 1 {
		span = 1
	}
	rise := 3 + int(storyHash(s.seed, uint64(s.spawnCount), uint64(w), uint64(h))%uint64(span))
	if rise > riseMax {
		rise = riseMax
	}
	// Keep fruit within a short climb so the scene reads as hunting,
	// not as a lucky long-distance grab.
	if rise > 8 {
		rise = 8
	}
	maxDX := rise - 2
	if limit := (w - 3) / 2; maxDX > limit {
		maxDX = limit
	}
	if maxDX < 0 {
		maxDX = 0
	}
	dx := 0
	if maxDX > 0 {
		dx = int(storyHash(s.seed, uint64(s.spawnCount)^0x51, uint64(s.head.x), uint64(s.head.y))%uint64(2*maxDX+1)) - maxDX
	}
	x := s.head.x + dx
	if x < 1 {
		x = 1
	}
	if x > w-2 {
		x = w - 2
	}
	y := s.head.y - rise
	if y < 1 {
		y = 1
	}
	s.apples = append(s.apples[:0], storyApple{x: x, y: y})
}

func (s *snakeScene) step(w, h int, now time.Duration) {
	if s.waiting {
		return
	}
	if len(s.apples) == 0 && s.head.y > 3 {
		s.spawnApple(w, h)
	}
	dx := 0
	if len(s.apples) > 0 {
		a := s.apples[0]
		if a.x > s.head.x {
			dx = 1
		} else if a.x < s.head.x {
			dx = -1
		}
	}
	nx := s.head.x + dx
	if nx < 1 {
		nx = 1
	}
	if nx > w-2 {
		nx = w - 2
	}
	ny := s.head.y - 1
	if ny <= 0 {
		s.apples = s.apples[:0]
	}

	// Slide the body along the path first, then trim the old tail.
	s.body = append(s.body, storyPoint{})
	copy(s.body[1:], s.body[:len(s.body)-1])
	s.body[0] = storyPoint{x: nx, y: ny}
	for len(s.body) > s.length {
		s.body = s.body[:len(s.body)-1]
	}
	s.head = s.body[0]

	if len(s.apples) > 0 && s.head.x == s.apples[0].x && s.head.y == s.apples[0].y {
		s.length += 2
		if s.length > storySnakeMaxLen {
			s.length = storySnakeMaxLen
		}
		s.biteX, s.biteY = s.apples[0].x, s.apples[0].y
		s.biteUntil = now + 500*time.Millisecond
		s.apples = s.apples[:0]
		s.spawnApple(w, h)
	} else if len(s.apples) > 0 && s.head.y < s.apples[0].y {
		s.apples = s.apples[:0]
	}

	if s.head.y < -s.length-2 {
		s.waiting = true
		s.waitUntil = now + 900*time.Millisecond
		s.apples = s.apples[:0]
	}
}

func (s *snakeScene) drawScene(c *artCanvas, now time.Duration) {
	w, h := c.w, c.h
	if h >= 2 {
		for x := 0; x < w; x++ {
			q := storyHash(s.seed, 7, uint64(x), uint64(h))
			switch {
			case q%3 == 0:
				c.put(x, h-1, '"', artLeaf)
			case q%5 == 0:
				c.put(x, h-1, '.', artMuted)
			}
		}
		for y := 1; y < h-1; y += 4 {
			q := storyHash(s.seed, 8, uint64(y), uint64(h))
			if q%2 == 0 {
				c.put(0, y, '"', artLeaf)
			}
			if q%3 == 0 {
				c.put(w-1, y, '.', artLeaf)
			}
		}
	}

	if now < s.biteUntil {
		c.put(s.biteX-1, s.biteY, ':', artFlower)
		c.put(s.biteX+1, s.biteY, '*', artFlower)
	}
	for _, a := range s.apples {
		c.put(a.x, a.y, '@', artDanger)
	}
	if !s.waiting {
		for i := len(s.body) - 1; i >= 0; i-- {
			p := s.body[i]
			ch := 'o'
			tone := artLeaf
			if i == 0 {
				ch, tone = 'O', artAccent
			} else if i == len(s.body)-1 {
				ch = '~'
			}
			c.put(p.x, p.y, ch, tone)
		}
	}
}
