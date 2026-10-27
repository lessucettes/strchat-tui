package tui

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// inputField keeps primitive identity stable while its private edit/undo buffers
// are replaced. InputField.SetText is undoable and cannot release those buffers.
// Replacing the embedded pointer also avoids copying tview's internal mutex.
type inputField struct {
	*tview.InputField
}

func (i *inputField) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	handler := i.InputField.InputHandler()
	return func(event *tcell.EventKey, setFocus func(tview.Primitive)) {
		handler(event, setFocus)
		if event.Key() == tcell.KeyRune && event.Rune() == 'f' && event.Modifiers() == tcell.ModAlt {
			// Native Alt+F stops on the final grapheme. Advance past it using
			// the editor's own Unicode-aware movement; at EOF this is a no-op.
			handler(tcell.NewEventKey(tcell.KeyRight, 0, tcell.ModNone), setFocus)
		}
	}
}

func (i *inputField) reset() {
	old := i.InputField
	focused := old.HasFocus()
	field := tview.NewInputField().SetLabel(old.GetLabel()).
		SetLabelStyle(old.GetLabelStyle()).SetFieldStyle(old.GetFieldStyle()).
		SetFieldWidth(old.GetFieldWidth())
	field.Box = old.Box // preserve geometry, title, border and capture identity
	old.Blur()
	i.InputField = field
	if focused {
		i.Focus(func(tview.Primitive) {})
	}
}

// Mouse capture must retain the stable primitive, not an obsolete inner field
// (for example when a keyboard clear happens during a text-selection drag).
// Selection exposes the private TextArea; completion focuses the InputField.
func (i *inputField) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	field := i.InputField
	handler := field.MouseHandler()
	return func(action tview.MouseAction, event *tcell.EventMouse, focus func(tview.Primitive)) (bool, tview.Primitive) {
		consumed, capture := handler(action, event, func(p tview.Primitive) {
			_, editor := p.(*tview.TextArea)
			if p == field || editor {
				p = i
			}
			focus(p)
		})
		_, editor := capture.(*tview.TextArea)
		if capture == field || editor {
			capture = i
		}
		return consumed, capture
	}
}
