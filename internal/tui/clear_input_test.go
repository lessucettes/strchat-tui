package tui

import (
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/lessucettes/strchat-tui/internal/client"
)

func TestClearDiscardsNativeUndoAndKeepsInputBindings(t *testing.T) {
	ui := newIdleTUI(t, 4)
	actions := make(chan client.UserAction, 4)
	ui.actionsChan = actions
	ui.restoreTheme("red-gold")
	primitive, box, style := ui.input, ui.input.Box, ui.input.GetFieldStyle()
	ui.input.SetText("pre-clear draft")
	ui.handleCommand("/clear")
	if ui.input != primitive || ui.input.Box != box || ui.input.GetFieldStyle() != style || ui.app.GetFocus() != primitive || !ui.input.HasFocus() {
		t.Fatal("reset changed input primitive identity, style, geometry or focus")
	}
	for range 4 {
		ui.input.InputHandler()(tcell.NewEventKey(tcell.KeyCtrlZ, 0, tcell.ModNone), func(tview.Primitive) {})
		if ui.input.GetText() != "" {
			t.Fatal("native undo recovered pre-clear input")
		}
	}
	ui.applyEvent(client.DisplayEvent{Type: "CLEARED"})
	ui.input.SetText("new input")
	ui.input.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), func(tview.Primitive) {})
	if len(actions) != 2 || (<-actions).Type != "CLEAR" {
		t.Fatal("unexpected clear/input actions")
	}
	if action := <-actions; action.Type != "SEND_MESSAGE" || action.Payload != "new input" {
		t.Fatal("new input lost its send binding")
	}
	ui.input.InputHandler()(tcell.NewEventKey(tcell.KeyCtrlP, 0, tcell.ModNone), func(tview.Primitive) {})
	if ui.input.GetText() != "new input" {
		t.Fatal("new input lost its history binding")
	}
}

func TestClearDuringInputMouseSelectionKeepsStableEditor(t *testing.T) {
	ui := newIdleTUI(t, 4)
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(screen.Fini)
	screen.SetSize(80, 5)
	ui.input.SetRect(0, 0, 80, 3)
	ui.input.SetText("pre-clear mouse draft")
	ui.input.Draw(screen)
	focus := func(p tview.Primitive) { ui.app.SetFocus(p) }
	consumed, capture := ui.input.MouseHandler()(tview.MouseLeftDown,
		tcell.NewEventMouse(5, 1, tcell.Button1, tcell.ModNone), focus)
	if !consumed || capture != ui.input || ui.app.GetFocus() != ui.input {
		t.Fatal("mouse selection exposed the private editor as focus or capture")
	}
	ui.handleCommand("/clear")
	capture.MouseHandler()(tview.MouseMove,
		tcell.NewEventMouse(9, 1, tcell.Button1, tcell.ModNone), focus)
	capture.MouseHandler()(tview.MouseLeftUp,
		tcell.NewEventMouse(9, 1, tcell.ButtonNone, tcell.ModNone), focus)
	ui.input.InputHandler()(tcell.NewEventKey(tcell.KeyCtrlZ, 0, tcell.ModNone), focus)
	if ui.input.GetText() != "" || ui.app.GetFocus() != ui.input {
		t.Fatal("post-clear mouse events reached a detached editor")
	}
}
