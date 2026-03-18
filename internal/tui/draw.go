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

	// Layout: DM item (if enabled) at the top, then joined views.
	dmItemCount := 0
	if t.dmTargetPubKey != "" {
		dmItemCount = 1
	}

	// Clamp current index in case panel size changed.
	if currentItem < 0 {
		currentItem = 0
	}

	if t.dmTargetPubKey != "" {
		t.chatListItems = append(t.chatListItems, chatListItem{
			kind:     chatListItemKindDM,
			dmPubKey: t.dmTargetPubKey,
			dmNick:   t.dmTargetNick,
		})

		nick := t.dmTargetNick
		if nick == "" {
			nick = t.dmTargetPubKey
			if len(nick) > 10 {
				nick = nick[:4] + "..." + nick[len(nick)-4:]
			}
		}

		t.chatList.AddItem(fmt.Sprintf(" ▶ DM %s", nick), "", 0, nil)
	}

	for i, view := range t.views {
		// Skip the dedicated DM view entry; we display DM as a dedicated top item.
		if t.dmTargetChat != "" && view.Name == t.dmTargetChat {
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
			kind:      chatListItemKindView,
			viewIndex: i,
		})

		t.chatList.AddItem(fmt.Sprintf(" %s %s", prefix, viewName), "", 0, nil)
	}

	// Prefer selecting active DM when DM is enabled, else select active view.
	targetIndex := 0
	if t.dmTargetPubKey != "" {
		targetIndex = 0
	} else if t.activeViewIndex >= 0 {
		targetIndex = t.activeViewIndex + dmItemCount
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

	for _, u := range t.chatUsers {
		// Colorize the user's name using the same palette as chat messages.
		// Keep everything on a single line (nick + short pubkey hash).
		colorTag := pubkeyToColor(u.PubKey, t.theme.nickPalette)
		short := u.ShortPubKey
		if short == "" {
			if len(u.PubKey) >= 4 {
				short = u.PubKey[len(u.PubKey)-4:]
			} else {
				short = "????"
			}
		}
		t.userList.AddItem(fmt.Sprintf(" %s%s[-]#%s", colorTag, u.Nick, short), "", 0, nil)
	}

	if currentItem >= 0 && currentItem < t.userList.GetItemCount() {
		t.userList.SetCurrentItem(currentItem)
	} else {
		t.userList.SetCurrentItem(0)
	}
}

// updateDetailsView refreshes the details panel, showing relays or group members.
func (t *tui) updateDetailsView() {
	t.detailsView.SetTitle(titleInfo)
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
	if item.kind == chatListItemKindView && item.viewIndex >= 0 && item.viewIndex < len(t.views) {
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
		// For DM we still show relay connectivity (same as non-group views).
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

	if t.dmTargetPubKey != "" {
		label := t.dmTargetNick
		if label == "" {
			label = t.dmTargetPubKey
			if len(label) > 10 {
				label = label[:4] + "..." + label[len(label)-4:]
			}
		}
		if youHash != "" {
			t.input.SetLabel(fmt.Sprintf("DM %s (you #%s) > ", label, youHash))
		} else {
			t.input.SetLabel(fmt.Sprintf("DM %s > ", label))
		}
	} else if t.nick != "" {
		if youHash != "" {
			t.input.SetLabel(fmt.Sprintf("%s #%s > ", t.nick, youHash))
		} else {
			t.input.SetLabel(fmt.Sprintf("%s > ", t.nick))
		}
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
		t.userList:    false,
		t.detailsView: false,
		t.output:      false,
		t.input:       false,
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
			if t.dmTargetPubKey != "" {
				hintText = fmt.Sprintf("[%[1]s]Enter[-]: Send DM | [%[1]s]Ctrl+P/N[-]: History | [%[1]s]Tab/Shift+Tab[-]: Cycle Focus | %s", highlight, baseHints)
			} else {
				hintText = fmt.Sprintf("[%[1]s]Enter[-]: Send | [%[1]s]Ctrl+P/N[-]: History | [%[1]s]Tab/Shift+Tab[-]: Cycle Focus | %s", highlight, baseHints)
			}
		case t.output:
			hintText = fmt.Sprintf("[%[1]s]`[-]: Maximize | [%[1]s]↑/↓[-]: Scroll | [%[1]s]Tab/Shift+Tab[-]: Cycle Focus | %s", highlight, baseHints)
		case t.detailsView:
			hintText = fmt.Sprintf("[%[1]s]↑/↓[-]: Scroll | [%[1]s]Tab/Shift+Tab[-]: Cycle Focus | %s", highlight, baseHints)
		case t.userList:
			hintText = fmt.Sprintf("[%[1]s]Enter[-]: Open DM | [%[1]s]Tab/Shift+Tab[-]: Cycle Focus | %s", highlight, baseHints)
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
