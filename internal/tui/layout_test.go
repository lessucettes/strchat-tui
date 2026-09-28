package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

func TestLayoutPlanFor(t *testing.T) {
	cases := []struct {
		name string
		w, h int
		want layoutPlan
	}{
		{"full 120x40", 120, 40, layoutPlan{mode: modeFull, logs: true, output: true, hints: true}},
		{"full exactly 100x24", 100, 24, layoutPlan{mode: modeFull, logs: true, output: true, hints: true}},
		{"narrow 99x24", 99, 24, layoutPlan{mode: modeNarrow, logs: true, output: true, hints: true}},
		{"narrow 80x24", 80, 24, layoutPlan{mode: modeNarrow, logs: true, output: true, hints: true}},
		{"tiny by width 59x24", 59, 24, layoutPlan{mode: modeTiny, output: true, hints: true}},
		{"tiny by height 80x11", 80, 11, layoutPlan{mode: modeTiny, output: true, hints: true}},
		{"tiny no hints 40x7", 40, 7, layoutPlan{mode: modeTiny, output: true}},
		{"tiny no output 40x4", 40, 4, layoutPlan{mode: modeTiny}},
		{"degenerate 1x1", 1, 1, layoutPlan{mode: modeTiny}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := layoutPlanFor(tc.w, tc.h); got != tc.want {
				t.Fatalf("layoutPlanFor(%d,%d) = %+v, want %+v", tc.w, tc.h, got, tc.want)
			}
		})
	}
}

func TestTinyScreenShowsInputAndMessages(t *testing.T) {
	s := newSimTUI(t, 40, 8)
	s.events <- testState()
	s.events <- testMessage(1)
	waitOutput(t, s, 1)
	s.draw()

	if !s.matches(func() bool { return s.ui.visible(s.ui.input) }) {
		t.Fatal("input is not visible on a tiny screen")
	}
	if !s.matches(func() bool { return s.ui.visible(s.ui.outputView) }) {
		t.Fatal("message view is not visible on a tiny screen")
	}
	if !s.matches(func() bool {
		return !s.ui.visible(s.ui.logsView) && !s.ui.visible(s.ui.chatList) && !s.ui.visible(s.ui.detailsView)
	}) {
		t.Fatal("hidden panes are still part of the tiny layout")
	}
	screen := s.text()
	if !strings.Contains(screen, "message 1") {
		t.Fatalf("message not visible on tiny screen:\n%s", screen)
	}
	if !strings.Contains(screen, "Alt+I") {
		t.Fatalf("input prompt missing on tiny screen:\n%s", screen)
	}
	if strings.Contains(screen, "Alt+L") || strings.Contains(screen, "Alt+C") {
		t.Fatalf("hidden panes still drawn on tiny screen:\n%s", screen)
	}
}

func TestDegenerateSizeStillRenders(t *testing.T) {
	s := newSimTUI(t, 12, 3)
	s.draw()
	if screen := s.text(); screen == "" {
		t.Fatal("empty screen at degenerate size")
	}
	if !s.matches(func() bool { return s.ui.visible(s.ui.input) }) {
		t.Fatal("input must stay visible at any size")
	}
}

func TestLayoutRecoversFromTinyToFull(t *testing.T) {
	s := newSimTUI(t, 40, 8)
	s.draw()
	if !s.matches(func() bool { return s.ui.plan.mode == modeTiny }) {
		t.Fatal("expected tiny layout at 40x8")
	}
	s.resize(120, 40)
	s.draw()
	if !s.matches(func() bool { return s.ui.plan.mode == modeFull }) {
		t.Fatal("layout did not recover to full after resize")
	}
	if !s.matches(func() bool { return s.ui.visible(s.ui.logsView) && s.ui.visible(s.ui.chatList) }) {
		t.Fatal("panes not restored after growing the terminal")
	}
	if screen := s.text(); !strings.Contains(screen, "Alt+L") || !strings.Contains(screen, "Alt+C") {
		t.Fatalf("full layout titles missing:\n%s", screen)
	}
}

func TestNarrowLayoutShowsSidebarBelowMessages(t *testing.T) {
	s := newSimTUI(t, 80, 24)
	s.draw()
	if !s.matches(func() bool { return s.ui.plan.mode == modeNarrow }) {
		t.Fatal("expected narrow layout at 80x24")
	}
	if !s.matches(func() bool { return s.ui.visible(s.ui.chatList) && s.ui.visible(s.ui.outputView) }) {
		t.Fatal("narrow layout must keep messages, chats and info visible")
	}
}

func TestFocusCyclingSkipsHiddenWidgets(t *testing.T) {
	s := newSimTUI(t, 40, 8)
	s.draw()
	s.onLoop(func() { s.ui.setFocus(s.ui.input) })
	s.key(tcell.KeyTab, 0, tcell.ModNone)
	waitFor(t, 2*time.Second, func() bool {
		return s.matches(func() bool { return s.ui.focused == s.ui.outputView })
	})
	s.key(tcell.KeyTab, 0, tcell.ModNone)
	waitFor(t, 2*time.Second, func() bool {
		return s.matches(func() bool { return s.ui.focused == s.ui.input })
	})
	if got := s.matches(func() bool { return s.ui.focused == s.ui.chatList }); got {
		t.Fatal("focus reached the hidden chat list")
	}
}

func TestAltKeysIgnoreHiddenWidgets(t *testing.T) {
	s := newSimTUI(t, 40, 8)
	s.draw()
	s.onLoop(func() { s.ui.setFocus(s.ui.input) })
	s.key(tcell.KeyRune, 'l', tcell.ModAlt)
	s.key(tcell.KeyRune, 'c', tcell.ModAlt)
	s.draw()
	if !s.matches(func() bool { return s.ui.focused == s.ui.input }) {
		t.Fatal("Alt+key moved focus to a hidden widget")
	}
	// Alt+I stays a no-op but valid, and the visible message pane is reachable.
	s.key(tcell.KeyRune, 'o', tcell.ModAlt)
	waitFor(t, 2*time.Second, func() bool {
		return s.matches(func() bool { return s.ui.focused == s.ui.outputView })
	})
}

func TestMaximizeIsDisabledOnTinyScreens(t *testing.T) {
	s := newSimTUI(t, 40, 8)
	s.draw()
	s.onLoop(func() { s.ui.setFocus(s.ui.outputView) })
	s.key(tcell.KeyRune, '`', tcell.ModNone)
	time.Sleep(50 * time.Millisecond)
	if s.matches(func() bool { return s.ui.maximizeModeOf() != maximizeNone }) {
		t.Fatal("maximize was entered on a tiny screen")
	}
}

func TestMaximizeRoundTripInSplitLayout(t *testing.T) {
	s := newSimTUI(t, 120, 40)
	s.draw()
	s.onLoop(func() { s.ui.setFocus(s.ui.outputView) })
	s.key(tcell.KeyRune, '`', tcell.ModNone)
	waitFor(t, 2*time.Second, func() bool {
		return s.matches(func() bool { return s.ui.maximizeModeOf() == maximizeMessages })
	})
	if !s.matches(func() bool { return s.ui.visible(s.ui.outputView) && !s.ui.visible(s.ui.input) }) {
		t.Fatal("maximized view is not the only visible pane")
	}
	s.key(tcell.KeyRune, '`', tcell.ModNone)
	waitFor(t, 2*time.Second, func() bool {
		return s.matches(func() bool { return s.ui.maximizeModeOf() == maximizeNone })
	})
	if !s.matches(func() bool { return s.ui.visible(s.ui.input) && s.ui.visible(s.ui.outputView) }) {
		t.Fatal("split layout not restored after leaving maximize")
	}
	s.draw()
	if screen := s.text(); !strings.Contains(screen, "message") && !strings.Contains(screen, "Messages") {
		t.Fatalf("message pane missing after restore:\n%s", screen)
	}
}

func TestHintsAdvertiseTinyAffordances(t *testing.T) {
	s := newSimTUI(t, 40, 8)
	s.draw()
	hints := onLoopValue(s, func() string { return s.ui.hints.GetText(true) })
	for _, want := range []string{"Tab", "/help", "Ctrl+C"} {
		if !strings.Contains(hints, want) {
			t.Fatalf("tiny hints %q missing %q", hints, want)
		}
	}
}

func TestHintsShowBackpressureNotice(t *testing.T) {
	ui := newIdleTUI(t, 1)
	got := hintsText(hintsState{highlight: "[#00ff00]", warn: "[#ffff00]", mode: modeFull, area: focusInput, notice: backpressureNotice(3, true)})
	if !strings.Contains(got, "client busy: 3 input(s) not sent") {
		t.Fatalf("hintsText = %q", got)
	}
	if noNotice := hintsText(hintsState{highlight: "[#00ff00]", warn: "[#ffff00]", mode: modeFull, area: focusInput}); strings.Contains(noNotice, "client busy") {
		t.Fatalf("hintsText shows a notice without backpressure: %q", noNotice)
	}
	_ = ui
}
