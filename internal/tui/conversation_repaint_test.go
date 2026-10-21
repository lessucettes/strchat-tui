package tui

import (
	"testing"
)

func TestComposerTextChangeSchedulesDestinationRepaint(t *testing.T) {
	ui := newIdleTUI(t, 16)
	ui.applyEvent(focusGroupState())
	ui.render()
	ui.input.SetText("@peer#beef hello")
	if !ui.dirty.Load() {
		t.Fatal("complete mention changed the composer without scheduling a destination repaint")
	}
}
