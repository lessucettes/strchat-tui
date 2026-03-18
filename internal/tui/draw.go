package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/lessucettes/strchat-tui/internal/client"
)

// updateChatList refreshes the chat list view, indicating the active and selected chats.
func (t *tui) updateChatList() {
	currentItem := t.chatList.GetCurrentItem()
	t.chatList.Clear()
	t.chatListItems = nil

	if currentItem < 0 {
		currentItem = 0
	}

	for i, view := range t.views {
		// Skip legacy DM-prefixed views (they were created by old DM logic).
		if strings.HasPrefix(view.Name, "DM-") {
			continue
		}
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

		t.chatListItems = append(t.chatListItems, chatListItem{
			viewIndex: i,
		})

		t.chatList.AddItem(fmt.Sprintf(" %s %s", prefix, viewName), "", 0, nil)
	}

	targetIndex := 0
	if t.activeViewIndex >= 0 {
		targetIndex = t.activeViewIndex
	}

	if targetIndex < 0 {
		targetIndex = 0
	}
	if targetIndex >= len(t.chatListItems) {
		targetIndex = len(t.chatListItems) - 1
	}
	if targetIndex >= 0 {
		t.chatList.SetCurrentItem(targetIndex)
	} else if currentItem >= 0 {
		t.chatList.SetCurrentItem(currentItem)
	}
}

// updateUserList refreshes the users panel for the currently active view.
func (t *tui) updateUserList() {
	currentItem := t.userList.GetCurrentItem()
	t.userList.Clear()

	if len(t.chatUsers) == 0 {
		return
	}

	sort.SliceStable(t.chatUsers, func(i, j int) bool {
		if t.chatUsers[i].Nick == t.chatUsers[j].Nick {
			return t.chatUsers[i].ShortPubKey < t.chatUsers[j].ShortPubKey
		}
		return t.chatUsers[i].Nick < t.chatUsers[j].Nick
	})

	for idx, u := range t.chatUsers {
		colorTag := pubkeyToNickColorTag(u.PubKey)
		short := u.ShortPubKey
		if short == "" {
			if len(u.PubKey) >= 4 {
				short = u.PubKey[len(u.PubKey)-4:]
			} else {
				short = "????"
			}
		}
		t.userList.AddItem(fmt.Sprintf("%2d %s%s[-]#%s", idx+1, colorTag, u.Nick, short), "", 0, nil)
	}

	if currentItem >= 0 && currentItem < t.userList.GetItemCount() {
		t.userList.SetCurrentItem(currentItem)
	} else {
		t.userList.SetCurrentItem(0)
	}
}

// updateDetailsView refreshes the details panel, showing relays or group members.
func (t *tui) updateDetailsView() {
	onlineCount := 0
	for _, r := range t.relays {
		if r.Connected {
			onlineCount++
		}
	}

	if t.narrowMode {
		t.detailsView.SetTitle(fmt.Sprintf("%s ONLINE: %d", titleInfoShort, onlineCount))
	} else {
		t.detailsView.SetTitle(fmt.Sprintf("%s ONLINE: %d", titleInfo, onlineCount))
	}
	t.detailsView.Clear()

	if t.chatList.GetItemCount() == 0 || len(t.chatListItems) == 0 {
		return
	}
	currentIndex := t.chatList.GetCurrentItem()
	if currentIndex >= len(t.chatListItems) || currentIndex < 0 {
		return
	}

	item := t.chatListItems[currentIndex]
	var selectedView *client.View
	if item.viewIndex >= 0 && item.viewIndex < len(t.views) {
		selectedView = &t.views[item.viewIndex]
	}

	if selectedView != nil && selectedView.IsGroup {
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
				var symbol string
				switch {
				case !r.Connected:
					statusColor = t.theme.logErrorColor
					symbol = "×"
				case r.Latency > 750*time.Millisecond:
					statusColor = t.theme.logWarnColor
					symbol = "●"
				default:
					statusColor = t.theme.titleColor
					symbol = "●"
				}
				host := strings.TrimPrefix(strings.TrimPrefix(r.URL, "wss://"), "ws://")
				builder.WriteString(fmt.Sprintf(" [%s]%s[-] %s\n", statusColor, symbol, host))
			}
		}
		fmt.Fprint(t.detailsView, builder.String())
	}
}

// updateInputLabel sets the prompt label for the input field, including the user's nick.
func (t *tui) updateInputLabel() {
	youHash := ""
	if t.selfShortPubKey != "" {
		youHash = t.selfShortPubKey
	}

	if t.nick != "" {
		if youHash != "" {
			t.input.SetLabel(fmt.Sprintf("%s#%s > ", t.nick, youHash))
		} else {
			t.input.SetLabel(fmt.Sprintf("%s > ", t.nick))
		}
	} else {
		t.input.SetLabel("> ")
	}
	t.updateInputTitle()
}

// updateInputTitle shows reply target in the input frame title when replying.
func (t *tui) updateInputTitle() {
	if t.pendingReply == nil {
		if t.narrowMode {
			t.input.SetTitle(titleInputShort)
		} else {
			t.input.SetTitle(titleInput)
		}
		return
	}
	ref := fmt.Sprintf("@%s#%s", t.pendingReply.Nick, t.pendingReply.ShortPubKey)
	if t.narrowMode {
		t.input.SetTitle(fmt.Sprintf("%s Reply %s Alt+Q", titleInputShort, ref))
	} else {
		t.input.SetTitle(fmt.Sprintf("%s Reply to %s: (Alt+Q) to cancel", titleInput, ref))
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
		t.userList:    false,
		t.detailsView: false,
		t.output:      false,
		t.input: false,
	}

	if _, ok := components[currentFocus]; ok {
		components[currentFocus] = true
	}

	t.logs.SetBorderColor(map[bool]tcell.Color{true: focusedColor, false: unfocusedColor}[components[t.logs]])
	t.chatList.SetBorderColor(map[bool]tcell.Color{true: focusedColor, false: unfocusedColor}[components[t.chatList]])
	t.userList.SetBorderColor(map[bool]tcell.Color{true: focusedColor, false: unfocusedColor}[components[t.userList]])
	t.detailsView.SetBorderColor(map[bool]tcell.Color{true: focusedColor, false: unfocusedColor}[components[t.detailsView]])
	t.output.SetBorderColor(map[bool]tcell.Color{true: focusedColor, false: unfocusedColor}[components[t.output]])
	t.input.SetBorderColor(map[bool]tcell.Color{true: focusedColor, false: unfocusedColor}[components[t.input]])

	// Only visually highlight the selected message when messages are focused.
	if t.app.GetFocus() == t.output {
		t.output.SetSelectedBackgroundColor(t.theme.borderColor)
	} else {
		t.output.SetSelectedBackgroundColor(t.theme.backgroundColor)
	}

	// Only visually highlight the selected user when Users panel is focused.
	if t.app.GetFocus() == t.userList {
		t.userList.SetSelectedBackgroundColor(t.theme.borderColor)
	} else {
		t.userList.SetSelectedBackgroundColor(t.theme.backgroundColor)
	}
}

// updateHints displays context-sensitive hints for the user.
func (t *tui) updateHints() {
	var hintText string
	highlight := t.theme.titleColor
	baseHints := fmt.Sprintf("[%[1]s]Alt+...[-]: Focus", highlight)

	// Keep selection highlight in sync with focus (important after maximize/minimize).
	if t.app.GetFocus() == t.output {
		t.output.SetSelectedBackgroundColor(t.theme.borderColor)
	} else {
		t.output.SetSelectedBackgroundColor(t.theme.backgroundColor)
	}

	if t.logsMaximized {
		hintText = fmt.Sprintf("[%[1]s]`[-]: Restore | [%[1]s]↑/↓[-]: Scroll", highlight)
	} else if t.outputMaximized {
		hintText = fmt.Sprintf("[%[1]s]`[-]: Restore | [%[1]s]↑/↓[-]: Scroll", highlight)
	} else {
		switch t.app.GetFocus() {
		case t.input:
			if t.pendingReply != nil {
				hintText = fmt.Sprintf("[%[1]s]Enter[-]: Send reply | [%[1]s]Alt+Q[-]: Cancel reply | [%[1]s]Ctrl+P/N[-]: History | %s", highlight, baseHints)
			} else {
				hintText = fmt.Sprintf("[%[1]s]Enter[-]: Send | [%[1]s]Ctrl+P/N[-]: History | [%[1]s]Tab/Shift+Tab[-]: Cycle Focus | %s", highlight, baseHints)
			}
		case t.output:
			hintText = fmt.Sprintf("[%[1]s]Enter[-]: @nick#id in input | [%[1]s]`[-]: Maximize | [%[1]s]↑/↓[-]: Scroll | [%[1]s]Tab/Shift+Tab[-]: Cycle | %s", highlight, baseHints)
		case t.detailsView:
			hintText = fmt.Sprintf("[%[1]s]↑/↓[-]: Scroll | [%[1]s]Tab/Shift+Tab[-]: Cycle Focus | %s", highlight, baseHints)
		case t.userList:
			hintText = fmt.Sprintf("[%[1]s]Enter[-]: Reply | [%[1]s]Tab/Shift+Tab[-]: Cycle Focus | %s", highlight, baseHints)
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
