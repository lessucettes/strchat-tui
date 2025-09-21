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
func (t *tui) updateChatList() {
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
func (t *tui) updateDetailsView() {
	t.detailsView.SetTitle(titleInfo)
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
		builder.WriteString(fmt.Sprintf(" [%s]Chats of %s:[-]\n", t.theme.logWarnColor, selectedView.Name))
		for _, child := range selectedView.Children {
			builder.WriteString(fmt.Sprintf(" - %s\n", child))
		}
		fmt.Fprint(t.detailsView, builder.String())
	} else {
		var builder strings.Builder
		builder.WriteString(fmt.Sprintf("[%s]Connected Relays:[-]\n", t.theme.logWarnColor))

		sort.SliceStable(t.relays, func(i, j int) bool {
			return t.relays[i].URL < t.relays[j].URL
		})

		if len(t.relays) == 0 {
			builder.WriteString(fmt.Sprintf(" [%s]Not connected...[-]\n", t.theme.logInfoColor))
		} else {
			for _, r := range t.relays {
				var statusColor tcell.Color
				if r.Latency > 750*time.Millisecond {
					statusColor = t.theme.logWarnColor
				} else {
					statusColor = t.theme.titleColor
				}
				host := strings.TrimPrefix(strings.TrimPrefix(r.URL, "wss://"), "ws://")
				builder.WriteString(fmt.Sprintf(" [%s]●[-] %s [-]\n",
					statusColor, host))
			}
		}
		fmt.Fprint(t.detailsView, builder.String())
	}
}

// updateInputLabel sets the prompt label for the input field, including the user's nick.
func (t *tui) updateInputLabel() {
	if t.nick != "" {
		t.input.SetLabel(fmt.Sprintf("%s > ", t.nick))
	} else {
		t.input.SetLabel("> ")
	}
}

// updateFocusBorders changes widget border colors to highlight the focused element.
func (t *tui) updateFocusBorders() {
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
func (t *tui) updateHints() {
	var hintText string
	highlight := t.theme.titleColor
	baseHints := fmt.Sprintf("[%[1]s]Alt+...[-]: Focus | [%[1]s]Ctrl+C[-]: Quit", highlight)

	if t.logsMaximized {
		hintText = fmt.Sprintf("[%[1]s]`[-]: Restore | [%[1]s]↑/↓[-]: Scroll | [%[1]s]Ctrl+C[-]: Quit", highlight)
	} else if t.outputMaximized {
		hintText = fmt.Sprintf("[%[1]s]`[-]: Restore | [%[1]s]↑/↓[-]: Scroll | [%[1]s]Ctrl+C[-]: Quit", highlight)
	} else {
		switch t.app.GetFocus() {
		case t.input:
			hintText = fmt.Sprintf("[%[1]s]Enter[-]: Send | [%[1]s]Ctrl+P/N[-]: History | [%[1]s]Tab/Shift+Tab[-]: Cycle Focus | %s", highlight, baseHints)
		case t.output:
			hintText = fmt.Sprintf("[%[1]s]`[-]: Maximize | [%[1]s]↑/↓[-]: Scroll | [%[1]s]Tab/Shift+Tab[-]: Cycle Focus | %s", highlight, baseHints)
		case t.detailsView:
			hintText = fmt.Sprintf("[%[1]s]↑/↓[-]: Scroll | [%[1]s]Tab/Shift+Tab[-]: Cycle Focus | %s", highlight, baseHints)
		case t.chatList:
			hintText = fmt.Sprintf("[%[1]s]Space[-]: Select | [%[1]s]Enter[-]: Activate | [%[1]s]Del[-]: Delete | [%[1]s]Tab/Shift+Tab[-]: Cycle Focus | %s", highlight, baseHints)
		case t.logs:
			hintText = fmt.Sprintf("[%[1]s]`[-]: Maximize | [%[1]s]Tab/Shift+Tab[-]: Cycle Focus | %s", highlight, baseHints)
		default:
			hintText = baseHints
		}
	}
	t.hints.SetText(hintText)
}
