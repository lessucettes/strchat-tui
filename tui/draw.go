// tui/draw.go
package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// updateChatList refreshes the chat list view, indicating the active and selected chats.
func (t *TUI) updateChatList() {
	currentItem := t.chatList.GetCurrentItem()
	t.chatList.Clear()
	if len(t.views) == 0 {
		return
	}

	for i, view := range t.views {
		var prefix string
		isActive := i == t.activeViewIndex
		isSelected := t.selectedForGroup[view.Name]

		if isActive && isSelected && !view.IsGroup {
			prefix = "⊛"
		} else if isActive {
			prefix = "▶"
		} else if isSelected {
			prefix = "⊕"
		} else {
			prefix = " "
		}

		viewName := view.Name
		if view.PoW > 0 {
			viewName = fmt.Sprintf("%s [PoW:%d]", view.Name, view.PoW)
		}

		t.chatList.AddItem(fmt.Sprintf(" %s %s", prefix, viewName), "", 0, nil)
	}

	if currentItem >= len(t.views) {
		currentItem = len(t.views) - 1
	}
	if currentItem < 0 {
		currentItem = 0
	}
	t.chatList.SetCurrentItem(currentItem)
}

// updateDetailsView refreshes the details panel, showing relays or group members.
func (t *TUI) updateDetailsView() {
	t.detailsView.SetTitle("Info (Alt+N)")
	t.detailsView.Clear()

	if t.chatList.GetItemCount() == 0 || len(t.views) == 0 {
		return
	}
	currentIndex := t.chatList.GetCurrentItem()
	if currentIndex >= len(t.views) || currentIndex < 0 {
		return
	}
	selectedView := t.views[currentIndex]

	if selectedView.IsGroup {
		var builder strings.Builder
		builder.WriteString(fmt.Sprintf(" [yellow]Chats of %s:[-]\n", selectedView.Name))
		for _, child := range selectedView.Children {
			builder.WriteString(fmt.Sprintf(" - %s\n", child))
		}
		fmt.Fprint(t.detailsView, builder.String())
	} else {
		var builder strings.Builder
		builder.WriteString("[yellow]Connected Relays:[-]\n")

		sort.SliceStable(t.relays, func(i, j int) bool {
			return t.relays[i].URL < t.relays[j].URL
		})

		if len(t.relays) == 0 {
			builder.WriteString(" [grey]Not connected...[-]\n")
		} else {
			for _, r := range t.relays {
				var statusColor string
				if r.Latency > 750*time.Millisecond {
					statusColor = "yellow"
				} else {
					statusColor = "green"
				}
				builder.WriteString(fmt.Sprintf(" [%s]●[-] %s [-]\n",
					statusColor, r.URL[6:]))
			}
		}
		fmt.Fprint(t.detailsView, builder.String())
	}
}

// updateInputLabel sets the prompt label for the input field, including the user's nick.
func (t *TUI) updateInputLabel() {
	if t.nick != "" {
		t.input.SetLabel(fmt.Sprintf("%s > ", t.nick))
	} else {
		t.input.SetLabel("> ")
	}
}

// updateFocusBorders changes widget border colors to highlight the focused element.
func (t *TUI) updateFocusBorders() {
	currentFocus := t.app.GetFocus()
	unfocusedColor := tview.Styles.BorderColor
	focusedColor := tview.Styles.TitleColor

	components := map[tview.Primitive]bool{
		t.logs:        false,
		t.chatList:    false,
		t.detailsView: false,
		t.output:      false,
		t.input:       false,
	}

	if _, ok := components[currentFocus]; ok {
		components[currentFocus] = true
	}

	t.logs.SetBorderColor(map[bool]tcell.Color{true: focusedColor, false: unfocusedColor}[components[t.logs]])
	t.chatList.SetBorderColor(map[bool]tcell.Color{true: focusedColor, false: unfocusedColor}[components[t.chatList]])
	t.detailsView.SetBorderColor(map[bool]tcell.Color{true: focusedColor, false: unfocusedColor}[components[t.detailsView]])
	t.output.SetBorderColor(map[bool]tcell.Color{true: focusedColor, false: unfocusedColor}[components[t.output]])
	t.input.SetBorderColor(map[bool]tcell.Color{true: focusedColor, false: unfocusedColor}[components[t.input]])
}

// updateHints displays context-sensitive hints for the user.
func (t *TUI) updateHints() {
	var hintText string
	baseHints := "[lime]Alt+...[-]: Focus | [lime]Ctrl+C[-]: Quit"

	if t.logsMaximized {
		hintText = "[lime]`[-]: Restore | [lime]↑/↓[-]: Scroll | [lime]Ctrl+C[-]: Quit"
	} else if t.outputMaximized {
		hintText = "[lime]`[-]: Restore | [lime]↑/↓[-]: Scroll | [lime]Ctrl+C[-]: Quit"
	} else {
		switch t.app.GetFocus() {
		case t.input:
			hintText = "[lime]Enter[-]: Send | [lime]Ctrl+P/N[-]: History | [lime]Tab/Shift+Tab[-]: Cycle Focus | " + baseHints
		case t.output:
			hintText = "[lime]`[-]: Maximize | [lime]↑/↓[-]: Scroll | [lime]Tab/Shift+Tab[-]: Cycle Focus | " + baseHints
		case t.detailsView:
			hintText = "[lime]↑/↓[-]: Scroll | [lime]Tab/Shift+Tab[-]: Cycle Focus | " + baseHints
		case t.chatList:
			hintText = "[lime]Space[-]: Select | [lime]Enter[-]: Activate | [lime]Del[-]: Delete | [lime]Tab/Shift+Tab[-]: Cycle Focus | " + baseHints
		case t.logs:
			hintText = "[lime]`[-]: Maximize | [lime]Tab/Shift+Tab[-]: Cycle Focus | " + baseHints
		default:
			hintText = baseHints
		}
	}
	t.hints.SetText(hintText)
}
