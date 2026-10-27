package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

func TestInputHistoryCtrlPN(t *testing.T) {
	s := newSimTUI(t, 120, 40)
	for _, text := range []string{"first message", "/help", "@alice#1234 hello"} {
		s.onLoop(func() { s.ui.input.SetText(text) })
		s.key(tcell.KeyEnter, 0, tcell.ModNone)
		waitFor(t, 2*time.Second, func() bool {
			return s.matches(func() bool { return s.ui.input.GetText() == "" })
		})
	}
	s.onLoop(func() { s.ui.input.SetText("unfinished draft") })
	for _, step := range []struct {
		key  tcell.Key
		want string
	}{
		{tcell.KeyCtrlP, "@alice#1234 hello"},
		{tcell.KeyCtrlP, "/help"},
		{tcell.KeyCtrlP, "first message"},
		{tcell.KeyCtrlN, "/help"},
		{tcell.KeyCtrlN, "@alice#1234 hello"},
		{tcell.KeyCtrlN, "unfinished draft"},
	} {
		s.key(step.key, 0, tcell.ModCtrl)
		waitFor(t, 2*time.Second, func() bool {
			return s.matches(func() bool { return s.ui.input.GetText() == step.want })
		})
		waitFor(t, 2*time.Second, func() bool { return strings.Contains(s.text(), step.want) })
	}
}

func TestInputEditingBindings(t *testing.T) {
	s := newSimTUI(t, 120, 40)
	key := func(k tcell.Key) *tcell.EventKey { return tcell.NewEventKey(k, 0, tcell.ModNone) }
	alt := func(r rune) *tcell.EventKey { return tcell.NewEventKey(tcell.KeyRune, r, tcell.ModAlt) }
	for _, reset := range []bool{false, true} {
		if reset {
			s.onLoop(func() { s.ui.handleCommand("/clear") })
		}
		for _, tc := range []struct {
			name string
			text string
			keys []*tcell.EventKey
			want string
		}{
			{"word left", "one two", []*tcell.EventKey{alt('b')}, "one Xtwo"},
			{"word right", "one two", []*tcell.EventKey{key(tcell.KeyHome), alt('f')}, "oneX two"},
			{"word right repeated", "one two", []*tcell.EventKey{key(tcell.KeyHome), alt('f'), alt('f')}, "one twoX"},
			{"word right at end", "one", []*tcell.EventKey{alt('f'), alt('f')}, "oneX"},
			{"word right empty", "", []*tcell.EventKey{alt('f')}, "X"},
			{"word right combining", "abе\u0301 tail", []*tcell.EventKey{key(tcell.KeyHome), alt('f')}, "abе\u0301X tail"},
			{"word right selection", "one two", []*tcell.EventKey{key(tcell.KeyHome), tcell.NewEventKey(tcell.KeyRight, 0, tcell.ModShift), alt('f')}, "oneX two"},
			{"delete word BS", "one two", []*tcell.EventKey{tcell.NewEventKey(tcell.KeyBackspace, 0, tcell.ModAlt)}, "one X"},
			{"delete word DEL", "one two", []*tcell.EventKey{tcell.NewEventKey(tcell.KeyBackspace2, 0, tcell.ModAlt)}, "one X"},
			{"delete word legacy BS", "one two", []*tcell.EventKey{tcell.NewEventKey(tcell.KeyRune, 'h', tcell.ModAlt|tcell.ModCtrl)}, "one X"},
			{"grapheme left", "aе\u0301🙂z", []*tcell.EventKey{key(tcell.KeyCtrlB), key(tcell.KeyCtrlB), key(tcell.KeyCtrlB)}, "aXе\u0301🙂z"},
			{"grapheme right", "aе\u0301🙂z", []*tcell.EventKey{key(tcell.KeyHome), key(tcell.KeyCtrlF), key(tcell.KeyCtrlF)}, "aе\u0301X🙂z"},
			{"Alt Enter does not send", "one two", []*tcell.EventKey{tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModAlt)}, "one twoX"},
			{"Alt Enter does not run commands", "/nick changed", []*tcell.EventKey{tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModAlt)}, "/nick changedX"},
			{"Alt Home is ignored", "one two", []*tcell.EventKey{tcell.NewEventKey(tcell.KeyHome, 0, tcell.ModAlt)}, "one twoX"},
		} {
			t.Run(fmt.Sprintf("%s/after_clear=%t", tc.name, reset), func(t *testing.T) {
				s.onLoop(func() { s.ui.input.SetText(tc.text) })
				s.draw()
				for _, ev := range tc.keys {
					s.key(ev.Key(), ev.Rune(), ev.Modifiers())
				}
				s.key(tcell.KeyRune, 'X', tcell.ModNone)
				waitFor(t, time.Second, func() bool {
					return s.matches(func() bool { return strings.Contains(s.ui.input.GetText(), "X") })
				})
				if got := onLoopValue(s, func() string { return s.ui.input.GetText() }); got != tc.want {
					t.Fatalf("input = %q, want %q", got, tc.want)
				}
			})
		}
	}
	// Pane shortcuts still take precedence over the editor's Alt bindings.
	s.key(tcell.KeyRune, 'o', tcell.ModAlt)
	waitFor(t, time.Second, func() bool { return s.matches(func() bool { return s.ui.focused == s.ui.outputView }) })
	s.key(tcell.KeyRune, 'i', tcell.ModAlt)
	waitFor(t, time.Second, func() bool { return s.matches(func() bool { return s.ui.focused == s.ui.input }) })
}

func TestInputHistoryBoundariesAndEditing(t *testing.T) {
	ui := newIdleTUI(t, 32)
	press := func(key tcell.Key) {
		ui.input.InputHandler()(tcell.NewEventKey(key, 0, tcell.ModNone), nil)
	}
	check := func(want string) {
		t.Helper()
		if got := ui.input.GetText(); got != want {
			t.Fatalf("input = %q, want %q", got, want)
		}
	}
	ui.input.SetText("draft")
	press(tcell.KeyCtrlP)
	check("draft")
	press(tcell.KeyCtrlN)
	check("draft")
	ui.input.SetText("   ")
	press(tcell.KeyEnter)
	if len(ui.inputHistory) != 0 {
		t.Fatal("blank input was retained")
	}
	for _, text := range []string{"one", "two"} {
		ui.input.SetText(text)
		press(tcell.KeyEnter)
	}
	ui.input.SetText("draft")
	press(tcell.KeyCtrlN)
	check("draft")
	press(tcell.KeyCtrlP)
	check("two")
	press(tcell.KeyCtrlP)
	press(tcell.KeyCtrlP)
	check("one")
	press(tcell.KeyCtrlN)
	check("two")
	ui.input.SetText("edited two")
	press(tcell.KeyCtrlN)
	check("draft")
	press(tcell.KeyCtrlN)
	check("draft")
	press(tcell.KeyCtrlP)
	check("two") // Browsing edits do not overwrite stored entries.
	ui.input.SetText("edited two")
	press(tcell.KeyEnter)
	check("")
	press(tcell.KeyCtrlP)
	check("edited two")
	press(tcell.KeyCtrlP)
	check("two")
	press(tcell.KeyCtrlN)
	press(tcell.KeyCtrlN)
	check("") // Enter reset the saved draft and navigation position.
}

func TestInputHistoryBoundAndBackpressure(t *testing.T) {
	// No receiver: messages remain in the input, but attempted lines are recallable.
	ui := newIdleTUI(t, 0)
	for i := 0; i < maxInputHistory+3; i++ {
		text := fmt.Sprintf("message %d", i)
		ui.input.SetText(text)
		ui.input.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), nil)
		if ui.input.GetText() != text {
			t.Fatal("backpressure cleared the input")
		}
	}
	if len(ui.inputHistory) != maxInputHistory || ui.inputHistory[0] != "message 3" {
		t.Fatalf("history did not evict the oldest entries: %v", ui.inputHistory)
	}
	ui.input.SetText("new draft")
	ui.input.InputHandler()(tcell.NewEventKey(tcell.KeyCtrlP, 0, tcell.ModNone), nil)
	if want := fmt.Sprintf("message %d", maxInputHistory+2); ui.input.GetText() != want {
		t.Fatalf("latest attempted input = %q, want %q", ui.input.GetText(), want)
	}
}
