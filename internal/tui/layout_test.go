package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/lessucettes/strchat-tui/internal/client"
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
		{"short narrow 80x12 prioritizes messages", 80, 12, layoutPlan{mode: modeTiny, output: true, hints: true}},
		{"short narrow 80x14 prioritizes messages", 80, 14, layoutPlan{mode: modeTiny, output: true, hints: true}},
		{"tiny by width 59x24", 59, 24, layoutPlan{mode: modeTiny, output: true, hints: true}},
		{"tiny by height 80x11", 80, 11, layoutPlan{mode: modeTiny, output: true, hints: true}},
		{"tiny no hints 40x7", 40, 7, layoutPlan{mode: modeTiny, output: true}},
		{"tiny too short for message content 40x5", 40, 5, layoutPlan{mode: modeTiny}},
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

func TestShortNarrowScreensKeepMessageRows(t *testing.T) {
	for _, tc := range []struct {
		name   string
		height int
	}{{"80x12", 12}, {"80x13", 13}, {"80x14", 14}} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSimTUI(t, 80, tc.height)
			s.events <- testState()
			s.events <- testMessage(1)
			waitOutput(t, s, 1)
			s.draw()
			if screen := s.text(); !strings.Contains(screen, "message 1") {
				t.Fatalf("message content missing at %s:\n%s", tc.name, screen)
			}
		})
	}
}

func TestInputLabelEscapesMarkupAndPreservesTypedText(t *testing.T) {
	t.Run("markup nickname is literal", func(t *testing.T) {
		s := newSimTUI(t, 80, 24)
		s.onLoop(func() {
			s.ui.applyEvent(client.DisplayEvent{Type: "STATE_UPDATE", Payload: client.StateUpdate{Views: []client.View{{Name: "chat"}}, ChatNicks: map[string]string{"chat": "[red]"}}})
			s.ui.input.SetText("typed")
			s.ui.render()
			s.ui.app.ForceDraw()
		})
		if screen := s.text(); !strings.Contains(screen, "[red] > ") {
			t.Fatalf("nickname markup was not shown literally:\n%s", screen)
		}
	})

	t.Run("long nickname leaves room for input", func(t *testing.T) {
		s := newSimTUI(t, 60, 24)
		s.onLoop(func() {
			s.ui.applyEvent(client.DisplayEvent{Type: "STATE_UPDATE", Payload: client.StateUpdate{Views: []client.View{{Name: "chat"}}, ChatNicks: map[string]string{"chat": strings.Repeat("n", 200)}}})
			s.ui.input.SetText("typed")
			s.ui.render()
			s.ui.app.ForceDraw()
		})
		if screen := s.text(); !strings.Contains(screen, "typed") {
			t.Fatalf("long nickname hid the typed input:\n%s", screen)
		}
	})
}

func TestQuitHintIsVisibleAtCommonWidths(t *testing.T) {
	for _, tc := range []struct {
		name  string
		width int
		chats bool
	}{{"80 columns with input focused", 80, false}, {"100 columns with chats focused", 100, true}} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSimTUI(t, tc.width, 24)
			if tc.chats {
				s.onLoop(func() { s.ui.setFocus(s.ui.chatList) })
			}
			s.draw()
			lines := strings.Split(strings.TrimRight(s.text(), "\n"), "\n")
			hintLine := lines[len(lines)-1]
			if !strings.Contains(hintLine, "Ctrl+D") {
				t.Fatalf("quit hint clipped at %d columns: %q", tc.width, hintLine)
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
	for _, want := range []string{"Tab", "/help", "Ctrl+D"} {
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
	if strings.Contains(got, "[[") || strings.Contains(got, "Ctrl+C") {
		t.Fatalf("hints contain nested color tags or an obsolete quit binding: %q", got)
	}
	if noNotice := hintsText(hintsState{highlight: "[#00ff00]", warn: "[#ffff00]", mode: modeFull, area: focusInput}); strings.Contains(noNotice, "client busy") {
		t.Fatalf("hintsText shows a notice without backpressure: %q", noNotice)
	}
	_ = ui
}
