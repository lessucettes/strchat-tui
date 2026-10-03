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
