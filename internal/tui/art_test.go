package tui

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/lessucettes/strchat-tui/internal/client"
)

// Listing scenes is read-only; selection is persisted by the client owner.
func TestArtCommandListsScenesWithoutClientActions(t *testing.T) {
	for _, command := range []string{"/art", "/a"} {
		t.Run(command, func(t *testing.T) {
			s := newSimTUI(t, 140, 32)
			s.onLoop(func() { s.ui.handleCommand(command) })
			s.draw()
			text := s.text()
			for _, name := range []string{"herbarium", "interference", "windmill", "skyscraper", "bob", "tank", "snake", "rain", "maze", "spacewars"} {
				if !strings.Contains(text, name) {
					t.Fatalf("%s did not list %q:\n%s", command, name, text)
				}
			}
			select {
			case action := <-s.actions:
				t.Fatalf("decoration sent a client action: %+v", action)
			default:
			}
		})
	}
}

func TestArtSelectionRestoresAndWaitsForSave(t *testing.T) {
	s := newSimTUI(t, 140, 32)
	s.ui.applyEvent(client.DisplayEvent{Type: "ART_UPDATE", Content: "off"})
	s.draw()
	if !s.matches(func() bool { return s.ui.art.name == "off" && !s.ui.artRunning.Load() }) {
		t.Fatal("saved art preference was not restored at startup")
	}
	for _, tc := range []struct{ command, name string }{
		{"/art rain", "rain"}, {"/a rain", "rain"}, {"/a random", "random"}, {"/a off", "off"},
	} {
		name := tc.name
		previous := onLoopValue(s, func() string { return s.ui.art.name })
		s.onLoop(func() {
			s.ui.art.elapsed = 3 * time.Second
			s.ui.handleCommand(tc.command)
		})
		select {
		case action := <-s.actions:
			if action.Type != "SET_ART" || action.Payload != name {
				t.Fatalf("selection submitted %+v", action)
			}
		default:
			t.Fatal("art selection did not request persistence")
		}
		if !s.matches(func() bool { return s.ui.art.name == previous && s.ui.art.elapsed >= 3*time.Second }) {
			t.Fatal("scene changed before the owner's successful save response")
		}
		s.ui.applyEvent(client.DisplayEvent{Type: "ART_UPDATE", Content: name})
		s.draw()
		if !s.matches(func() bool {
			selected := s.ui.art.name == name
			if name == "random" {
				selected = s.ui.art.scene != nil && s.ui.art.name != "random"
			}
			return selected && s.ui.art.elapsed < time.Second
		}) {
			t.Fatalf("saved %s was not applied or repeated selection did not restart it", name)
		}
		s.onLoop(func() { s.ui.art.elapsed = 3 * time.Second })
		s.draw()
		if !s.matches(func() bool { return s.ui.art.elapsed >= 3*time.Second }) {
			t.Fatal("ordinary repaint restarted the selected scene")
		}
	}
}

// Animation must work without incoming events and stop while hidden. Toggling
// the ornament must return its width without changing drafts or navigation.
func TestArtColumnVisibilityAnimationAndInputIsolation(t *testing.T) {
	s := newSimTUI(t, 140, 32)
	s.onLoop(func() {
		s.ui.applyEvent(client.DisplayEvent{Type: "ART_UPDATE", Content: "rain"})
		s.ui.render()
		s.ui.input.SetText("draft stays here")
	})
	s.draw()
	if !s.matches(func() bool {
		mx, _, mw, _ := s.ui.outputView.GetRect()
		ax, _, aw, _ := s.ui.art.GetRect()
		cx, _, _, _ := s.ui.chatList.GetRect()
		return s.ui.artRunning.Load() && ax == mx+mw && aw == artWidth && cx == ax+aw
	}) {
		t.Fatal("decoration is not between Messages and Chats")
	}
	frame := func() []artCell {
		return onLoopValue(s, func() []artCell { return append([]artCell(nil), s.ui.art.canvas.cells...) })
	}
	before := frame()
	waitFor(t, 2*time.Second, func() bool { return !reflect.DeepEqual(before, frame()) })
	point := onLoopValue(s, func() [2]int { x, y, _, _ := s.ui.art.GetRect(); return [2]int{x + 2, y + 2} })
	clickMouse(s, point[0], point[1])
	s.key(tcell.KeyRune, '!', tcell.ModNone)
	waitFor(t, time.Second, func() bool {
		return s.matches(func() bool { return s.ui.focused == s.ui.input && s.ui.input.GetText() == "draft stays here!" })
	})
	widthWithArt := onLoopValue(s, func() int { _, _, w, _ := s.ui.outputView.GetRect(); return w })
	s.ui.applyEvent(client.DisplayEvent{Type: "ART_UPDATE", Content: "off"})
	s.draw()
	if !s.matches(func() bool {
		_, _, w, _ := s.ui.outputView.GetRect()
		return !s.ui.artRunning.Load() && w == widthWithArt+artWidth
	}) {
		t.Fatal("/art off did not stop animation and return its width")
	}
	s.onLoop(func() {
		s.ui.applyEvent(client.DisplayEvent{Type: "ART_UPDATE", Content: "rain"})
		s.ui.render()
		s.ui.art.elapsed = 3 * time.Second
		s.ui.handleCommand("/art invalid")
		if s.ui.art.name != "rain" || s.ui.art.elapsed != 3*time.Second {
			t.Error("invalid selection replaced or restarted the current scene")
		}
	})
	s.draw()
	s.resize(119, 32)
	s.draw()
	if s.ui.artRunning.Load() {
		t.Fatal("art still running below its width threshold")
	}
	paused := onLoopValue(s, func() time.Duration { return s.ui.art.elapsed })
	s.draw()
	if !s.matches(func() bool { return s.ui.art.elapsed == paused }) {
		t.Fatal("hidden scene continued advancing")
	}
	s.resize(140, 32)
	s.draw()
	waitFor(t, time.Second, s.ui.artRunning.Load)
	s.onLoop(func() { s.ui.setMaximized(maximizeMessages) })
	s.draw()
	if s.ui.artRunning.Load() {
		t.Fatal("art still running in maximized Messages")
	}
	s.onLoop(func() { s.ui.setMaximized(maximizeNone) })
	s.draw()
	if !s.matches(func() bool {
		return s.ui.artRunning.Load() && s.ui.focused == s.ui.outputView && s.ui.input.GetText() == "draft stays here!"
	}) {
		t.Fatal("restoring art changed input or focus")
	}
	s.onLoop(func() { s.ui.setFocus(s.ui.input) })
	s.key(tcell.KeyTab, 0, tcell.ModNone)
	waitFor(t, time.Second, func() bool { return s.matches(func() bool { return s.ui.focused == s.ui.chatList }) })
	select {
	case action := <-s.actions:
		t.Fatalf("art changed client state: %+v", action)
	default:
	}
}

// The little stories must depict coherent actions, not just changing glyphs.
func TestArtStoriesInteractions(t *testing.T) {
	t.Run("tank drives forward", func(t *testing.T) {
		scene := newStoryScene("tank", 42)
		var a, b artCanvas
		a.reset(16, 25)
		b.reset(16, 25)
		scene.draw(&a, 100*time.Millisecond)
		scene.draw(&b, 350*time.Millisecond)
		// A tank facing up sees stationary roadside landmarks move down.
		for y := 0; y < a.h-1; y++ {
			if a.cells[y*a.w] != b.cells[(y+1)*b.w] {
				t.Fatal("roadside is not moving down while the tank drives up")
			}
		}
	})
	t.Run("tank reacts to visible targets and hits them", func(t *testing.T) {
		for _, seed := range []uint64{1, 42, 999} {
			scene := newStoryScene("tank", seed)
			var c artCanvas
			previousEnemy, previousShell := storyPoint{-1, -1}, storyPoint{-1, -1}
			previousAim, hits, turns, sideTargets := 0, 0, 0, 0
			var appeared time.Duration
			fired := false
			for frame := 0; frame < 960; frame++ {
				at := time.Duration(frame) * 50 * time.Millisecond
				c.reset(16, 25)
				scene.draw(&c, at)
				cx, tankTop := c.w/2, c.h-7
				hitY := tankTop / 2
				enemy, shell := storyPoint{-1, -1}, storyPoint{-1, -1}
				for i, cell := range c.cells {
					if cell == (artCell{'o', artDanger}) {
						enemy = storyPoint{i % c.w, i / c.w}
					}
					if cell.tone == artWarm && strings.ContainsRune("|/\\", cell.ch) {
						shell = storyPoint{i % c.w, i / c.w}
					}
				}
				if enemy.x >= 0 && previousEnemy.x < 0 {
					appeared, fired = at, false
					if enemy.x != cx {
						sideTargets++
					}
				}
				aim := 0
				if c.cells[tankTop*c.w+cx-2] == (artCell{'\\', artAccent}) {
					aim = -1
				} else if c.cells[tankTop*c.w+cx+2] == (artCell{'/', artAccent}) {
					aim = 1
				}
				if aim != previousAim && aim != 0 {
					if enemy.x < 0 || at-appeared < 200*time.Millisecond || (enemy.x-cx)*aim <= 0 {
						t.Fatalf("seed=%d at=%v: turret anticipated an invisible target or aimed away", seed, at)
					}
					turns++
				}
				fired = fired || shell.x >= 0
				if enemy.x < 0 && previousEnemy.x >= 0 {
					if !fired || c.cells[hitY*c.w+previousEnemy.x] != (artCell{'*', artWarm}) {
						t.Fatal("target disappeared without a shell impact and explosion")
					}
					if previousShell.x < 0 || absArt(previousShell.x-previousEnemy.x) > 1 || absArt(previousShell.y-hitY) > 1 {
						t.Fatal("shell missed the target's explosion position")
					}
					hits++
				}
				previousEnemy, previousShell, previousAim = enemy, shell, aim
			}
			if hits < 10 {
				t.Fatal("tank stopped shooting or removing targets")
			}
			if turns == 0 || turns != sideTargets {
				t.Fatalf("turret did not acquire every side target: turns=%d targets=%d", turns, sideTargets)
			}
		}
	})
	t.Run("tank target sequence varies across attacks", func(t *testing.T) {
		for _, seed := range []uint64{1, 42, 999} {
			scene := newStoryScene("tank", seed)
			var c artCanvas
			var lanes []int
			var times []time.Duration
			visible := false
			for frame := 0; frame < 960; frame++ {
				at := time.Duration(frame) * 50 * time.Millisecond
				c.reset(16, 25)
				scene.draw(&c, at)
				x := -1
				for i, cell := range c.cells {
					if cell == (artCell{'o', artDanger}) {
						x = i % c.w
					}
				}
				if x >= 0 && !visible {
					lanes = append(lanes, x)
					times = append(times, at)
				}
				visible = x >= 0
			}
			seen := make(map[int]bool)
			intervals := make(map[time.Duration]bool)
			varied := false
			for i, lane := range lanes {
				seen[lane] = true
				if i > 0 {
					intervals[times[i]-times[i-1]] = true
				}
				if i >= 2 && lane != lanes[i-2] {
					varied = true
				}
			}
			if len(seen) < 3 || !seen[8] || !varied || len(intervals) < 3 {
				t.Fatalf("seed=%d: repetitive target sequence: lanes=%v times=%v", seed, lanes, times)
			}
		}
	})
	t.Run("snake enters from below on every cycle", func(t *testing.T) {
		for _, size := range [][2]int{{3, 4}, {16, 8}, {16, 25}} {
			for _, seed := range []uint64{1, 42, 999} {
				scene := newStoryScene("snake", seed)
				var c artCanvas
				previousCount, entrances := 0, 0
				for frame := 0; frame < 200; frame++ {
					c.reset(size[0], size[1])
					scene.draw(&c, time.Duration(frame)*storySnakeTick)
					count, headY := 0, -1
					for i, cell := range c.cells {
						if cell == (artCell{'O', artAccent}) {
							count++
							headY = i / c.w
						} else if cell == (artCell{'o', artLeaf}) || cell == (artCell{'~', artLeaf}) {
							count++
						}
					}
					if frame == 0 && count != 0 {
						t.Fatalf("size=%v seed=%d: snake appeared inside the viewport", size, seed)
					}
					if count > previousCount+1 {
						t.Fatalf("size=%v seed=%d frame=%d: body popped in (%d -> %d)", size, seed, frame, previousCount, count)
					}
					if previousCount == 0 && count > 0 {
						if count != 1 || headY != c.h-1 {
							t.Fatalf("size=%v seed=%d frame=%d: snake did not enter head-first at the bottom", size, seed, frame)
						}
						entrances++
					}
					previousCount = count
				}
				if entrances < 2 {
					t.Fatalf("size=%v seed=%d: did not verify repeated entry", size, seed)
				}
			}
		}
	})
	t.Run("snake keeps growing as it eats apples", func(t *testing.T) {
		for _, height := range []int{25, 64} {
			for _, seed := range []uint64{1, 42, 999} {
				scene := newStoryScene("snake", seed)
				var c artCanvas
				apple := storyPoint{-1, -1}
				previousY, eaten := height, 0
				growAt, beforeGrowth := -1, 0
				for frame := 0; frame < height+3; frame++ {
					c.reset(16, height)
					scene.draw(&c, time.Duration(frame)*storySnakeTick)
					head, nextApple := storyPoint{-1, -1}, storyPoint{-1, -1}
					segments := 0
					for i, cell := range c.cells {
						switch cell {
						case artCell{'O', artAccent}:
							head = storyPoint{i % c.w, i / c.w}
							segments++
						case artCell{'o', artLeaf}, artCell{'~', artLeaf}:
							segments++
						case artCell{'@', artDanger}:
							nextApple = storyPoint{i % c.w, i / c.w}
						}
					}
					if head.x >= 0 {
						if head.y > previousY {
							t.Fatal("snake moved down before leaving the screen")
						}
						previousY = head.y
					}
					if frame == growAt && segments != beforeGrowth+2 {
						t.Fatalf("height=%d seed=%d apple=%d: visible body did not grow by two segments (%d -> %d)", height, seed, eaten, beforeGrowth, segments)
					}
					if head == apple && apple.x >= 0 && nextApple != apple {
						eaten++
						// Growth retains the tail over the next two steps. Check
						// before the head exits, when clipping cannot hide it.
						if head.y >= 3 {
							growAt, beforeGrowth = frame+2, segments
						}
					}
					apple = nextApple
				}
				if eaten == 0 || (height == 64 && eaten < 5) {
					t.Fatalf("height=%d seed=%d: too few apples to exercise continued growth: %d", height, seed, eaten)
				}
			}
		}
	})
}

// Herbarium must not restore rejected broad leaves; Bob must never reach an
// endpoint, and the windmill must stay distinct from the rain's water motif.
func TestArtSceneComposition(t *testing.T) {
	t.Run("spacewars fighters enter diagonally and exchange lethal fire", func(t *testing.T) {
		gaps, places := make(map[time.Duration]bool), make(map[storyPoint]bool)
		var destroyed [2]int
		for _, seed := range []uint64{1, 42, 999} {
			scene := newStoryScene("spacewars", seed)
			if scene == nil {
				t.Fatal("spacewars scene is missing")
			}
			var c artCanvas
			previous := [2]storyPoint{{-1, -1}, {-1, -1}}
			var previousShots, earlierShots []storyPoint
			var entries [2]int
			var lastEntry [2]time.Duration
			kills, shots := 0, 0
			for frame := 0; frame < 768; frame++ {
				at := time.Duration(frame) * 125 * time.Millisecond
				c.reset(16, 25)
				scene.draw(&c, at)
				ships := [2]storyPoint{{-1, -1}, {-1, -1}}
				var beams []storyPoint
				for i, cell := range c.cells {
					p := storyPoint{i % c.w, i / c.w}
					team := -1
					if cell == (artCell{'O', artAccent}) {
						team = 0
					} else if cell == (artCell{'O', artFlower}) {
						team = 1
					}
					if team >= 0 {
						if ships[team].x >= 0 {
							t.Fatal("distant fighter was duplicated")
						}
						ships[team] = p
					}
					if cell == (artCell{'/', artWarm}) {
						beams = append(beams, p)
						shots++
					}
				}
				for team, p := range ships {
					prev := previous[team]
					if p.x >= 0 && prev.x < 0 {
						if (team == 0 && (p.x > c.w/3 || p.y < c.h*2/3)) ||
							(team == 1 && (p.x < c.w*2/3 || p.y > c.h/3)) {
							t.Fatalf("team=%d: fighter appeared away from its diagonal entry corner: %v", team, p)
						}
						if entries[team] > 0 {
							gaps[at-lastEntry[team]] = true
						}
						entries[team]++
						lastEntry[team] = at
					}
					if p.x >= 0 && prev.x >= 0 && (absArt(p.x-prev.x) > 1 || absArt(p.y-prev.y) > 2) {
						t.Fatal("fighter teleported during a maneuver")
					}
					if p.x < 0 && prev.x > 1 && prev.x < c.w-2 && prev.y > 1 && prev.y < c.h-2 {
						impact, incoming := false, false
						for y := prev.y - 1; y <= prev.y+1; y++ {
							for x := prev.x - 1; x <= prev.x+1; x++ {
								if c.cells[y*c.w+x] == (artCell{'*', artWarm}) {
									impact = true
									places[storyPoint{x, y}] = true
								}
							}
						}
						// Rounding can put the last pulse underneath the target's
						// hull, so retain the preceding visible frame as well.
						for _, beam := range append(previousShots, earlierShots...) {
							incoming = incoming || (absArt(beam.x-prev.x) <= 3 && absArt(beam.y-prev.y) <= 3)
						}
						if !impact || !incoming {
							t.Fatalf("seed=%d at=%v: fighter vanished without an incoming shot and explosion", seed, at)
						}
						kills++
						destroyed[team]++
					}
				}
				previous, earlierShots, previousShots = ships, previousShots, beams
			}
			if kills < 3 || shots == 0 || entries[0] < 3 || entries[1] < 3 {
				t.Fatal("missing repeated encounters, gunfire or destroyed fighters")
			}
		}
		if destroyed[0] == 0 || destroyed[1] == 0 || len(gaps) < 2 || len(places) < 3 {
			t.Fatal("encounter timings, explosion positions or losing sides never vary")
		}
	})
	t.Run("maze forms connected passages then crumbles and regenerates", func(t *testing.T) {
		for _, size := range [][2]int{{7, 9}, {16, 25}, {16, 65}} {
			for _, seed := range []uint64{1, 42, 999} {
				scene := newStoryScene("maze", seed)
				if scene == nil {
					t.Fatal("maze scene is missing")
				}
				var c artCanvas
				snapshot := func(at time.Duration) []artCell {
					c.reset(size[0], size[1])
					scene.draw(&c, at)
					return append([]artCell(nil), c.cells...)
				}
				forming, full := snapshot(time.Second), snapshot(3*time.Second)
				if reflect.DeepEqual(forming, full) || !reflect.DeepEqual(full, snapshot(4*time.Second)) {
					t.Fatal("maze does not build progressively and hold its completed layout")
				}
				// Inspect the rendered corridors, not the generator's internal graph.
				seen := make([]bool, len(full))
				queue := []storyPoint{{0, 1}}
				seen[c.w] = true
				for head := 0; head < len(queue); head++ {
					p := queue[head]
					for _, d := range [...]storyPoint{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
						x, y := p.x+d.x, p.y+d.y
						if x < 0 || x >= c.w || y < 0 || y >= c.h {
							continue
						}
						i := y*c.w + x
						if !seen[i] && full[i].ch <= ' ' {
							seen[i] = true
							queue = append(queue, storyPoint{x, y})
						}
					}
				}
				rooms, edges := 0, 0
				for y := 1; y < c.h-1; y += 2 {
					for x := 1; x < c.w-1; x += 3 {
						rooms++
						if !seen[y*c.w+x] {
							t.Fatalf("size=%v seed=%d: maze has an unreachable room", size, seed)
						}
						if x+3 < c.w-1 && full[y*c.w+x+2].ch <= ' ' {
							edges++
						}
						if y+2 < c.h-1 && full[(y+1)*c.w+x].ch <= ' ' {
							edges++
						}
					}
				}
				if edges != rooms-1 || !seen[(c.h-2)*c.w+c.w-1] {
					t.Fatal("maze lacks a complete entrance-to-exit spanning tree")
				}
				crumbling := snapshot(7500 * time.Millisecond)
				walls, fragments := 0, 0
				for _, cell := range crumbling {
					if strings.ContainsRune("+-|", cell.ch) {
						walls++
					}
					if cell.ch == '.' || cell.ch == ':' {
						fragments++
					}
				}
				if walls == 0 || fragments == 0 || reflect.DeepEqual(crumbling, snapshot(8*time.Second)) {
					t.Fatal("maze disappears instead of breaking into moving fragments")
				}
				for _, cell := range snapshot(10 * time.Second) {
					if cell.ch > ' ' {
						t.Fatal("old maze fragments survive into the quiet gap")
					}
				}
				if next := snapshot(14 * time.Second); size[0] == 16 && reflect.DeepEqual(full, next) {
					t.Fatal("new cycle repeats the previous maze")
				}
			}
		}
	})
	t.Run("skyscraper clouds have varied silhouettes and cover the moon", func(t *testing.T) {
		starts := make(map[string]bool)
		shapes, widths := make(map[string]bool), make(map[int]bool)
		for seed := uint64(1); seed <= 16; seed++ {
			scene := newStoryScene("skyscraper", seed)
			var c artCanvas
			frames := make(map[string]bool)
			covered, exposed := false, false
			for second := 0; second <= 96; second++ {
				c.reset(16, 25)
				scene.draw(&c, time.Duration(second)*time.Second)
				var cloud strings.Builder
				minX, minY, maxX, maxY := c.w, 4, -1, -1
				for y := 0; y < 4; y++ {
					for x := 0; x < c.w; x++ {
						cell := c.cells[y*c.w+x]
						if cell.tone == artMuted && (cell.ch == '(' || cell.ch == ')') {
							t.Fatal("cloud still has a closed moon-like outline")
						}
						if cell.tone == artMuted && strings.ContainsRune("_-/\\`' ", cell.ch) {
							cloud.WriteRune(cell.ch)
							minX, minY = min(minX, x), min(minY, y)
							maxX, maxY = max(maxX, x), max(maxY, y)
						} else {
							cloud.WriteByte(' ')
						}
					}
				}
				frames[cloud.String()] = true
				// Normalize fully visible silhouettes so translation alone cannot
				// masquerade as different cloud shapes.
				if minX > 0 && maxX < c.w-1 && maxX >= minX {
					var shape strings.Builder
					for y := minY; y <= maxY; y++ {
						shape.WriteString(cloud.String()[y*c.w+minX : y*c.w+maxX+1])
						shape.WriteByte('\n')
					}
					shapes[shape.String()] = true
					widths[maxX-minX+1] = true
				}
				if second == 0 {
					starts[cloud.String()] = true
				}
				visible := 0
				for _, p := range []struct {
					x, y int
					ch   rune
				}{{12, 0, '_'}, {13, 0, '_'}, {11, 1, '('}, {14, 1, ')'}, {12, 2, '-'}, {13, 2, '-'}} {
					if c.cells[p.y*c.w+p.x] == (artCell{p.ch, artWarm}) {
						visible++
					}
				}
				covered = covered || visible < 6
				exposed = exposed || visible == 6
				for _, x := range []int{12, 13} {
					if cell := c.cells[c.w+x]; cell.ch == '*' || cell.ch == '.' {
						t.Fatalf("seed=%d second=%d: star %q shines through the moon interior at x=%d", seed, second, cell.ch, x)
					}
				}
			}
			if len(frames) < 4 {
				t.Fatal("clouds are missing or not drifting")
			}
			if !covered || !exposed {
				t.Fatalf("seed=%d: cloud must cover the moon and reveal it again", seed)
			}
		}
		if len(starts) < 4 {
			t.Fatal("clouds always start in the same place")
		}
		if len(shapes) < 8 || len(widths) < 4 {
			t.Fatalf("clouds still use near-identical templates: shapes=%d widths=%d", len(shapes), len(widths))
		}
	})
	t.Run("herbarium has only small foliage", func(t *testing.T) {
		for seed := uint64(1); seed <= 16; seed++ {
			scene := newHerbariumScene(seed)
			var c artCanvas
			for _, at := range []time.Duration{0, 3 * time.Second, 6 * time.Second} {
				c.reset(16, 25)
				scene.draw(&c, at)
				for _, cell := range c.cells {
					if cell.ch == '_' && cell.tone == artLeaf {
						t.Fatal("herbarium still contains broad outlined leaves")
					}
				}
			}
		}
	})
	t.Run("herbarium stems stay rooted", func(t *testing.T) {
		for _, size := range [][2]int{{16, 8}, {16, 13}, {16, 25}, {16, 64}} {
			for seed := uint64(1); seed <= 16; seed++ {
				for _, at := range []time.Duration{0, 3 * time.Second, 6 * time.Second} {
					var c artCanvas
					c.reset(size[0], size[1])
					newHerbariumScene(seed).draw(&c, at)
					// Read the opening from the rendered rim, not from the pane center.
					rimY, left, right := -1, c.w, -1
					for y := c.h - 4; y < c.h && rimY < 0; y++ {
						for x := 0; x < c.w; x++ {
							if c.cells[y*c.w+x] == (artCell{'_', artFlower}) {
								rimY, left, right = y, min(left, x), max(right, x)
							}
						}
					}
					if rimY < 1 {
						t.Fatal("herbarium pot rim is missing")
					}
					for x := 0; x < c.w; x++ {
						cell := c.cells[(rimY-1)*c.w+x]
						if cell.tone == artMuted && strings.ContainsRune("|/\\", cell.ch) && (x < left || x > right) {
							t.Fatalf("stem misses pot opening: size=%v seed=%d time=%v x=%d opening=%d..%d", size, seed, at, x, left, right)
						}
					}
					if c.h == 25 && c.cells[(rimY-2)*c.w+left-1] == (artCell{'\\', artLeaf}) {
						t.Fatal("herbarium retains the fixed bright leaf beside the rim")
					}
					connected := make([]bool, len(c.cells))
					var queue []int
					for x := 0; x < c.w; x++ {
						i := (c.h-1)*c.w + x
						if c.cells[i].ch > ' ' {
							connected[i] = true
							queue = append(queue, i)
						}
					}
					for next := 0; next < len(queue); next++ {
						i := queue[next]
						x, y := i%c.w, i/c.w
						for dy := -1; dy <= 1; dy++ {
							for dx := -1; dx <= 1; dx++ {
								xx, yy := x+dx, y+dy
								if xx < 0 || xx >= c.w || yy < 0 || yy >= c.h {
									continue
								}
								j := yy*c.w + xx
								cell := c.cells[j]
								if !connected[j] && cell.ch > ' ' && (yy >= rimY || cell.tone == artMuted) {
									connected[j] = true
									queue = append(queue, j)
								}
							}
						}
					}
					for i, cell := range c.cells {
						if cell.tone == artMuted && (cell.ch == '|' || cell.ch == '/' || cell.ch == '\\') && !connected[i] {
							t.Fatalf("floating stem: size=%v seed=%d time=%v at (%d,%d)", size, seed, at, i%c.w, i/c.w)
						}
					}
				}
			}
		}
	})
	t.Run("bob climbs without reaching a treetop", func(t *testing.T) {
		scene := newStoryScene("bob", 42)
		var c artCanvas
		previous := storyPoint{-1, -1}
		for _, at := range []time.Duration{0, 14 * time.Second, 16 * time.Second, 30 * time.Second, 120 * time.Second} {
			c.reset(16, 25)
			scene.draw(&c, at)
			head := storyPoint{-1, -1}
			for y := 0; y < c.h; y++ {
				for x := 0; x < c.w; x++ {
					if c.cells[y*c.w+x] == (artCell{'o', artAccent}) {
						head = storyPoint{x, y}
					}
				}
			}
			if head.x < 0 || (previous.x >= 0 && head.y != previous.y) {
				t.Fatal("Bob disappeared, reached an endpoint, or jumped back to the roots")
			}
			previous = head
		}
		var before, after artCanvas
		before.reset(16, 25)
		after.reset(16, 25)
		scene.draw(&before, 0)
		scene.draw(&after, 650*time.Millisecond)
		changed := false
		// Compare above Bob so limb poses cannot masquerade as world motion.
		for y := 0; y < 8; y++ {
			for x := 0; x < before.w; x++ {
				a := before.cells[y*before.w+x]
				b := after.cells[(y+1)*after.w+x]
				if a != b {
					t.Fatal("tree did not scroll down with stable branches and bark")
				}
				changed = changed || a != after.cells[y*after.w+x]
			}
		}
		if !changed {
			t.Fatal("Bob is moving his limbs against a static tree")
		}
	})
	t.Run("bob crosses the trunk without teleporting", func(t *testing.T) {
		starts := make(map[int]bool)
		for seed := uint64(1); seed <= 16; seed++ {
			scene := newStoryScene("bob", seed)
			var c artCanvas
			previous := storyPoint{-1, -1}
			left, right, crossing := false, false, false
			for at := time.Duration(0); at <= 40*time.Second; at += artInterval {
				c.reset(16, 25)
				scene.draw(&c, at)
				head := storyPoint{-1, -1}
				for i, cell := range c.cells {
					if cell == (artCell{'o', artAccent}) {
						if head.x >= 0 {
							t.Fatal("Bob has more than one head")
						}
						head = storyPoint{i % c.w, i / c.w}
					}
				}
				if head.x < 0 || (previous.x >= 0 && (absArt(head.x-previous.x) > 1 || head.y != previous.y)) {
					t.Fatalf("seed %d at %v: Bob disappeared or jumped from %v to %v", seed, at, previous, head)
				}
				if at == 0 {
					starts[head.x] = true
				}
				left = left || head.x < 7
				right = right || head.x > 8
				crossing = crossing || head.x == 7 || head.x == 8
				previous = head
			}
			if !left || !right || !crossing {
				t.Fatalf("seed %d: left=%v right=%v crossing=%v", seed, left, right, crossing)
			}
		}
		if len(starts) != 2 {
			t.Fatal("Bob always starts on the same side")
		}
	})
	t.Run("rain is open water with swimming fish", func(t *testing.T) {
		for _, h := range []int{8, 25, 64} {
			for seed := uint64(1); seed <= 8; seed++ {
				scene := newRainScene(seed)
				var c artCanvas
				fishPositions := make(map[int]bool)
				for frame := 0; frame < 48; frame++ {
					c.reset(16, h)
					scene.draw(&c, time.Duration(frame)*250*time.Millisecond)
					for i, cell := range c.cells {
						if cell.ch == 0 {
							continue
						}
						if cell.tone == artLeaf || cell.tone == artFlower || cell.ch == '#' || cell.ch == '^' || cell.ch == '_' || cell.ch == '/' || cell.ch == '\\' {
							t.Fatalf("rain still contains vegetation or scenery: %q", cell.ch)
						}
						if cell.tone == artAccent && cell.ch == 'o' {
							if i/c.w <= h*2/3 {
								t.Fatal("fish swam above the water surface")
							}
							fishPositions[i] = true
						}
					}
				}
				if h >= 25 && len(fishPositions) < 4 {
					t.Fatal("fish are missing or not swimming")
				}
			}
		}
	})
	t.Run("rain drops hit the surface before making ripples", func(t *testing.T) {
		r := &natRain{splashes: [8]natRainSplash{{x: 0.5, period: 4}}}
		// Other streams have not reached the surface during these samples.
		for i := 1; i < len(r.splashes); i++ {
			r.splashes[i] = natRainSplash{x: 0, period: 100}
		}
		var c artCanvas
		c.reset(16, 25)
		r.drawRain(&c, 2.5, 16)
		if c.cells[15*c.w+8].ch != '|' || c.cells[16*c.w+8].ch != 0 {
			t.Fatal("drop did not approach the surface from above")
		}
		c.reset(16, 25)
		r.drawRain(&c, 2.66, 16)
		if c.cells[15*c.w+7].ch != '\'' || c.cells[15*c.w+9].ch != '.' {
			t.Fatal("impact did not scatter separate droplets at the surface")
		}
		c.reset(16, 25)
		r.drawRain(&c, 2.82, 16)
		if c.cells[14*c.w+6].ch != '\'' || c.cells[15*c.w+10].ch != '.' || c.cells[15*c.w+7].ch != 0 {
			t.Fatal("splash droplets did not move up and outward")
		}
		c.reset(16, 25)
		r.drawRain(&c, 3.1, 16)
		for y := 14; y < 16; y++ {
			for x := 5; x <= 11; x++ {
				if c.cells[y*c.w+x].ch != 0 {
					t.Fatal("splash droplets remained suspended above the water")
				}
			}
		}
		c.reset(16, 25)
		r.drawRain(&c, 3.2, 16)
		if c.cells[16*c.w+6].ch != '(' || c.cells[16*c.w+10].ch != ')' {
			t.Fatal("splash did not expand into a ripple")
		}
	})
	t.Run("windmill windows stay in the wall below the eaves", func(t *testing.T) {
		for _, size := range [][2]int{{7, 15}, {16, 15}, {16, 16}, {16, 17}, {16, 25}} {
			var c artCanvas
			c.reset(size[0], size[1])
			// Horizontal sails leave the window row visible for this layout check.
			(&natWindmill{seed: 42}).draw(&c, 0)
			windows := 0
			for i, cell := range c.cells {
				if cell != (artCell{'[', artWarm}) {
					continue
				}
				row, x := i/c.w, i%c.w
				left, right := false, false
				for col := 0; col < c.w; col++ {
					if c.cells[row*c.w+col] == (artCell{'|', artMuted}) {
						left, right = left || col < x, right || col > x+2
					}
				}
				if !left || !right {
					t.Fatalf("size=%v: window is not enclosed by tower walls", size)
				}
				windows++
			}
			if windows != 1 {
				t.Fatalf("size=%v: missing windmill window", size)
			}
		}
	})
	t.Run("windmill flocks turn glide and fly without popping", func(t *testing.T) {
		heights, poses := make(map[int]bool), make(map[string]bool)
		peak, occupied, totalFrames, turns, longestGlide := 0, 0, 0, 0, 0
		for _, seed := range []uint64{1, 42, 999} {
			scene := newNatureScene("windmill", seed)
			var c artCanvas
			var previous []storyPoint
			var lastDX, glide []int
			flights, quiet := 0, 0
			for frame := 0; frame < 720; frame++ {
				totalFrames++
				c.reset(16, 25)
				scene.draw(&c, time.Duration(frame)*125*time.Millisecond)
				var birds []storyPoint
				var flat []bool
				for i, cell := range c.cells {
					if cell == (artCell{'v', artMuted}) {
						p := storyPoint{i % c.w, i / c.w}
						if p.y >= 9 {
							t.Fatal("bird flew into the windmill instead of the open sky")
						}
						birds = append(birds, p)
						heights[p.y] = true
						level := false
						if p.x > 0 && p.x < c.w-1 {
							pose := string([]rune{c.cells[i-1].ch, cell.ch, c.cells[i+1].ch})
							poses[pose] = true
							level = pose == "-v-"
						}
						flat = append(flat, level)
					}
				}
				peak = max(peak, len(birds))
				if len(birds) > 4 {
					t.Fatal("birds overcrowded the windmill scene")
				}
				if len(birds) == 0 {
					quiet++
				} else {
					occupied++
					if len(previous) == 0 {
						flights++
					}
				}
				// Check arrivals and departures independently: returning birds may
				// leave through their entry edge, but never pop in or out mid-sky.
				for direction, points := range [][]storyPoint{birds, previous} {
					others := previous
					if direction == 1 {
						others = birds
					}
					for _, p := range points {
						continuous := p.x == 0 || p.x == c.w-1
						for _, q := range others {
							continuous = continuous || (absArt(p.x-q.x) <= 1 && absArt(p.y-q.y) <= 1)
						}
						if !continuous {
							t.Fatalf("seed=%d frame=%d: bird popped mid-flight at %v", seed, frame, p)
						}
					}
				}
				stable := len(birds) == len(previous)
				for i, p := range birds {
					if stable && (absArt(p.x-previous[i].x) > 1 || absArt(p.y-previous[i].y) > 1) {
						stable = false
					}
				}
				if !stable {
					lastDX, glide = make([]int, len(birds)), make([]int, len(birds))
				}
				for i, p := range birds {
					if stable {
						dx := p.x - previous[i].x
						if dx != 0 {
							if lastDX[i] != 0 && lastDX[i] != dx {
								turns++
							}
							lastDX[i] = dx
						}
					}
					if flat[i] {
						glide[i]++
						longestGlide = max(longestGlide, glide[i])
					} else {
						glide[i] = 0
					}
				}
				previous = birds
			}
			if flights < 3 || quiet == 0 {
				t.Fatalf("seed=%d: missing repeated flights or quiet gaps", seed)
			}
		}
		if peak < 3 || turns == 0 || longestGlide < 8 || len(heights) < 3 || len(poses) < 3 || occupied*5 < totalFrames*2 {
			t.Fatalf("missing natural flock activity: peak=%d turns=%d glide_frames=%d heights=%d poses=%d occupied=%d", peak, turns, longestGlide, len(heights), len(poses), occupied)
		}
		for _, size := range [][2]int{{7, 3}, {16, 3}, {16, 5}} {
			var c artCanvas
			for frame := 0; frame < 240; frame++ {
				c.reset(size[0], 25)
				(&natWindmill{seed: 42}).drawBirds(&c, time.Duration(frame)*125*time.Millisecond, size[1])
				for i, cell := range c.cells {
					if cell.ch > ' ' && i/c.w >= size[1] {
						t.Fatal("flock escaped the compact sky band")
					}
				}
			}
		}
	})
	t.Run("windmill is grounded and has rotating sails", func(t *testing.T) {
		for _, h := range []int{8, 25, 64} {
			for _, seed := range []uint64{1, 42, 999} {
				scene := newNatureScene("windmill", seed)
				var c artCanvas
				var previousHub storyPoint
				rotors := make(map[string]bool)
				for frame := 0; frame < 24; frame++ {
					c.reset(16, h)
					scene.draw(&c, time.Duration(frame)*375*time.Millisecond)
					hub := storyPoint{-1, -1}
					var rotor strings.Builder
					for i, cell := range c.cells {
						if cell.ch != 0 && (cell.tone == artWater || strings.ContainsRune("~()*", cell.ch)) {
							t.Fatalf("windmill scene retained water or sky clutter: %q", cell.ch)
						}
						if cell == (artCell{'O', artWarm}) {
							if hub.x >= 0 {
								t.Fatal("windmill has multiple rotor hubs")
							}
							hub = storyPoint{i % c.w, i / c.w}
						}
						if cell.tone == artAccent {
							rotor.WriteRune(cell.ch)
						} else {
							rotor.WriteByte(' ')
						}
					}
					if hub.x < 0 || (frame > 0 && hub != previousHub) {
						t.Fatalf("size=16x%d seed=%d: windmill missing or moving around", h, seed)
					}
					if c.cells[(h-3)*c.w+hub.x].ch != '_' {
						t.Fatal("windmill is not anchored to the ground")
					}
					if frame == 0 {
						var ground artCanvas
						ground.reset(c.w, c.h)
						(&natWindmill{seed: seed}).drawGround(&ground, hub.x)
						if ground.cells[(h-3)*ground.w+hub.x].ch <= ' ' {
							t.Fatal("terrain does not meet the windmill base")
						}
					}
					rotors[rotor.String()] = true
					previousHub = hub
				}
				if len(rotors) < 4 {
					t.Fatal("windmill sails are missing or not rotating")
				}
			}
		}
	})
}

// All advertised scenes must draw printable animated output, including after
// degenerate sizes, resize, and a large elapsed-time jump.
func TestArtScenesStayBoundedAndAnimate(t *testing.T) {
	for _, name := range artNames {
		t.Run(name, func(t *testing.T) {
			for _, seed := range []uint64{1, 42, 999} {
				scene := newNatureScene(name, seed)
				if scene == nil {
					scene = newStoryScene(name, seed)
				}
				if scene == nil {
					t.Fatalf("missing scene %s", name)
				}
				var c artCanvas
				c.reset(16, 25)
				scene.draw(&c, 0)
				first := append([]artCell(nil), c.cells...)
				animated, drawn := false, false
				for frame := 1; frame <= 48; frame++ {
					c.reset(16, 25)
					scene.draw(&c, time.Duration(frame)*250*time.Millisecond)
					animated = animated || !reflect.DeepEqual(first, c.cells)
					for _, cell := range c.cells {
						drawn = drawn || cell.ch > ' '
						if cell.ch != 0 && (cell.ch < ' ' || cell.ch > '~') {
							t.Fatalf("non-ASCII glyph %q", cell.ch)
						}
						if cell.tone > artDanger {
							t.Fatalf("invalid tone %d", cell.tone)
						}
					}
				}
				if !drawn || !animated {
					t.Fatalf("seed %d: drawn=%v animated=%v", seed, drawn, animated)
				}
				for _, size := range [][2]int{{0, 0}, {1, 1}, {5, 3}, {16, 256}, {16, 25}} {
					c.reset(size[0], size[1])
					scene.draw(&c, 365*24*time.Hour)
					if len(c.cells) != c.w*c.h {
						t.Fatal("unbounded canvas")
					}
				}
			}
		})
	}
}
