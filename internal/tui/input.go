package tui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/lessucettes/strchat-tui/internal/client"
)

// setupHandlers configures all the logic for handling user input.
func (t *tui) setupHandlers() {
	// Configure the handler for the main input field.
	t.input.SetDoneFunc(func(key tcell.Key) {
		if key != tcell.KeyEnter {
			return
		}
		defer t.input.SetText("")

		text := strings.TrimSpace(t.input.GetText())
		if text == "" {
			return
		}

		if strings.HasPrefix(text, "/") {
			t.handleCommand(text)
		} else {
			t.actionsChan <- client.UserAction{Type: "SEND_MESSAGE", Payload: text}
		}

		// Logic to add the recipient to the recent recipients list.
		if !strings.HasPrefix(text, "/") {
			nick, complete := extractNickPrefix(text)
			if complete {
				nick = strings.TrimPrefix(nick, "@")
				for i, n := range t.recentRecipients {
					if n == nick {
						t.recentRecipients = append(t.recentRecipients[:i], t.recentRecipients[i+1:]...)
						break
					}
				}
				t.recentRecipients = append([]string{nick}, t.recentRecipients...)
				if len(t.recentRecipients) > 20 {
					t.recentRecipients = t.recentRecipients[:20]
				}
			}
		}
	})

	// Configure recipient history navigation with Ctrl+P/N.
	t.input.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyCtrlP || ev.Key() == tcell.KeyCtrlN {
			if len(t.recentRecipients) == 0 {
				return ev
			}

			if ev.Key() == tcell.KeyCtrlP {
				t.rrIdx = (t.rrIdx + 1) % len(t.recentRecipients)
			} else {
				if t.rrIdx <= 0 {
					t.rrIdx = len(t.recentRecipients) - 1
				} else {
					t.rrIdx--
				}
			}

			t.input.SetText("@" + t.recentRecipients[t.rrIdx] + " ")
			return nil
		}

		t.rrIdx = -1
		return ev
	})

	// Set up global key handlers for focus, exiting, etc.
	t.app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if t.logsMaximized || t.outputMaximized {
			return t.handleMaximizedViewKeys(event)
		}

		switch event.Key() {
		case tcell.KeyTab:
			t.cycleFocus(true)
			return nil
		case tcell.KeyBacktab:
			t.cycleFocus(false)
			return nil
		}

		if event.Modifiers() == tcell.ModAlt {
			switch event.Rune() {
			case 'c':
				t.app.SetFocus(t.chatList)
			case 'u':
				t.app.SetFocus(t.userList)
			case 'o':
				t.app.SetFocus(t.output)
			case 'i':
				t.app.SetFocus(t.input)
			case 'l':
				t.app.SetFocus(t.logs)
			case 'n':
				t.app.SetFocus(t.detailsView)
			}
			t.updateFocusBorders()
			t.updateHints()
			return nil
		}

		currentFocus := t.app.GetFocus()

		if currentFocus == t.chatList {
			return t.handleChatListKeys(event)
		}
		if currentFocus == t.userList {
			return t.handleUserListKeys(event)
		}

		if currentFocus == t.logs && event.Key() == tcell.KeyRune && event.Rune() == '`' {
			t.logsMaximized = true
			t.app.SetRoot(t.maximizedLogsFlex, true).SetFocus(t.logs)
			t.updateHints()
			return nil
		}

		if currentFocus == t.output && event.Key() == tcell.KeyRune && event.Rune() == '`' {
			t.outputMaximized = true
			t.app.SetRoot(t.maximizedOutputFlex, true).SetFocus(t.output)
			t.updateHints()
			return nil
		}

		if event.Key() == tcell.KeyCtrlC {
			t.actionsChan <- client.UserAction{Type: "QUIT"}
			return nil
		}

		return event
	})

	t.chatList.SetChangedFunc(func(index int, mainText string, secondaryText string, shortcut rune) {
		t.updateDetailsView()
	})
}

// handleCommand parses and dispatches actions for slash-commands.
func (t *tui) handleCommand(text string) {
	parts := strings.SplitN(text, " ", 2)
	command := parts[0]
	payload := ""
	if len(parts) > 1 {
		payload = parts[1]
	}
	switch command {
	case "/quit", "/q":
		t.actionsChan <- client.UserAction{Type: "QUIT"}
	case "/join", "/j":
		if payload != "" {
			t.actionsChan <- client.UserAction{Type: "JOIN_CHATS", Payload: payload}
		}
	case "/pow", "/p":
		if payload != "" {
			t.actionsChan <- client.UserAction{Type: "SET_POW", Payload: payload}
		} else {
			t.actionsChan <- client.UserAction{Type: "SET_POW", Payload: "0"}
		}
	case "/list", "/l":
		t.actionsChan <- client.UserAction{Type: "LIST_CHATS"}
	case "/set", "/s":
		args := strings.Fields(payload)
		switch len(args) {
		case 0:
			t.actionsChan <- client.UserAction{Type: "GET_ACTIVE_CHAT"}
		case 1:
			t.actionsChan <- client.UserAction{Type: "ACTIVATE_VIEW", Payload: args[0]}
		default:
			groupMembers := strings.Join(args, ",")
			t.actionsChan <- client.UserAction{Type: "CREATE_GROUP", Payload: groupMembers}
		}
	case "/nick", "/n":
		t.actionsChan <- client.UserAction{Type: "SET_NICK", Payload: payload}
	case "/del", "/d":
		t.actionsChan <- client.UserAction{Type: "DELETE_VIEW", Payload: payload}
	case "/block", "/b":
		if payload == "" {
			t.actionsChan <- client.UserAction{Type: "LIST_BLOCKED"}
		} else {
			t.actionsChan <- client.UserAction{Type: "BLOCK_USER", Payload: payload}
		}
	case "/unblock", "/ub":
		if payload == "" {
			t.actionsChan <- client.UserAction{Type: "LIST_BLOCKED"}
		} else {
			t.actionsChan <- client.UserAction{Type: "UNBLOCK_USER", Payload: payload}
		}
	case "/filter", "/f":
		t.actionsChan <- client.UserAction{Type: "HANDLE_FILTER", Payload: payload}
	case "/unfilter", "/uf":
		if payload == "" {
			t.actionsChan <- client.UserAction{Type: "CLEAR_FILTERS"}
		} else {
			t.actionsChan <- client.UserAction{Type: "REMOVE_FILTER", Payload: payload}
		}
	case "/mute", "/m":
		t.actionsChan <- client.UserAction{Type: "HANDLE_MUTE", Payload: payload}
	case "/unmute", "/um":
		if payload == "" {
			t.actionsChan <- client.UserAction{Type: "CLEAR_MUTES"}
		} else {
			t.actionsChan <- client.UserAction{Type: "REMOVE_MUTE", Payload: payload}
		}
	case "/relay", "/r":
		t.actionsChan <- client.UserAction{Type: "MANAGE_ANCHORS", Payload: payload}
	case "/boop", "/sound":
		arg := strings.ToLower(strings.TrimSpace(payload))
		if arg == "" {
			arg = "off"
		}

		switch arg {
		case "on", "enable", "enabled":
			t.boopEnabled = true
			fmt.Fprintf(t.logs, "\n[%s]Boop sound enabled[-]", t.theme.logInfoColor)
		case "off", "disable", "disabled", "0":
			t.boopEnabled = false
			fmt.Fprintf(t.logs, "\n[%s]Boop sound disabled[-]", t.theme.logWarnColor)
		default:
			fmt.Fprintf(t.logs, "\n[%s]ERROR: Usage: /boop on|off[-]", t.theme.logErrorColor)
		}
		t.logs.ScrollToEnd()
		t.updateHints()
		return
	case "/dm", "/pm":
		t.actionsChan <- client.UserAction{Type: "DM_USER", Payload: payload}
	case "/help", "/h":
		t.actionsChan <- client.UserAction{Type: "GET_HELP"}
	}
}

// cycleFocus cycles the focus between the main UI primitives.
func (t *tui) cycleFocus(forward bool) {
	primitives := []tview.Primitive{t.input, t.chatList, t.userList, t.output, t.logs, t.detailsView}
	for i, p := range primitives {
		if p.HasFocus() {
			var next int
			if forward {
				next = (i + 1) % len(primitives)
			} else {
				next = (i - 1 + len(primitives)) % len(primitives)
			}
			t.app.SetFocus(primitives[next])
			t.updateFocusBorders()
			t.updateHints()
			return
		}
	}
}

// handleMaximizedViewKeys handles key events when a view is maximized.
func (t *tui) handleMaximizedViewKeys(event *tcell.EventKey) *tcell.EventKey {
	currentFocus := t.app.GetFocus()
	switch event.Key() {
	case tcell.KeyRune:
		if event.Rune() == '`' {
			if currentFocus == t.logs {
				t.logsMaximized = false
				t.app.SetRoot(t.mainFlex, true).SetFocus(t.logs)
			}
			if currentFocus == t.output {
				t.outputMaximized = false
				t.app.SetRoot(t.mainFlex, true).SetFocus(t.output)
			}
			t.updateHints()
			return nil
		}
	case tcell.KeyCtrlC:
		t.actionsChan <- client.UserAction{Type: "QUIT"}
		return nil
	case tcell.KeyTab, tcell.KeyBacktab:
		return nil
	case tcell.KeyUp, tcell.KeyDown, tcell.KeyPgUp, tcell.KeyPgDn, tcell.KeyHome, tcell.KeyEnd:
		return event
	}
	return nil
}

// handleChatListKeys handles key events for the chat list view.
func (t *tui) handleChatListKeys(event *tcell.EventKey) *tcell.EventKey {
	if key := event.Key(); key == tcell.KeyUp || key == tcell.KeyDown || key == tcell.KeyHome || key == tcell.KeyEnd {
		return event
	}

	count := t.chatList.GetItemCount()
	if count == 0 || len(t.chatListItems) == 0 {
		return event
	}

	cur := t.chatList.GetCurrentItem()
	if cur < 0 || cur >= len(t.chatListItems) {
		return event
	}

	item := t.chatListItems[cur]
	switch event.Key() {
	case tcell.KeyRune:
		if event.Rune() == ' ' {
			if item.kind == chatListItemKindView && item.viewIndex >= 0 && item.viewIndex < len(t.views) {
				selectedView := t.views[item.viewIndex]
				if !selectedView.IsGroup {
					if t.selectedForGroup[selectedView.Name] {
						delete(t.selectedForGroup, selectedView.Name)
					} else {
						t.selectedForGroup[selectedView.Name] = true
					}
					t.updateChatList()
				}
			}
			return nil
		}
	case tcell.KeyEnter:
		switch item.kind {
		case chatListItemKindDM:
			// Toggle DM off.
			t.dmTargetPubKey = ""
			t.dmTargetNick = ""
			t.dmTargetChat = ""
			t.actionsChan <- client.UserAction{Type: "SET_DM_TARGET", Payload: ""}
			t.updateChatList()
			return nil
		case chatListItemKindView:
			selectedView := t.views[item.viewIndex]
			if len(t.selectedForGroup) > 1 {
				var members []string
				for name := range t.selectedForGroup {
					members = append(members, name)
				}
				t.actionsChan <- client.UserAction{Type: "CREATE_GROUP", Payload: strings.Join(members, ",")}
			} else {
				t.actionsChan <- client.UserAction{Type: "ACTIVATE_VIEW", Payload: selectedView.Name}
			}
			t.selectedForGroup = make(map[string]bool)
			return nil
		}
	case tcell.KeyDelete:
		switch item.kind {
		case chatListItemKindDM:
			t.dmTargetPubKey = ""
			t.dmTargetNick = ""
			t.dmTargetChat = ""
			t.actionsChan <- client.UserAction{Type: "SET_DM_TARGET", Payload: ""}
			t.updateChatList()
			return nil
		case chatListItemKindView:
			selectedView := t.views[item.viewIndex]
			action := "LEAVE_CHAT"
			if selectedView.IsGroup {
				action = "DELETE_GROUP"
			}
			t.actionsChan <- client.UserAction{Type: action, Payload: selectedView.Name}
			return nil
		}
	}
	return event
}

// handleUserListKeys handles key events for the user list view.
func (t *tui) handleUserListKeys(event *tcell.EventKey) *tcell.EventKey {
	if key := event.Key(); key == tcell.KeyUp || key == tcell.KeyDown || key == tcell.KeyHome || key == tcell.KeyEnd {
		return event
	}

	cur := t.userList.GetCurrentItem()
	if cur < 0 || cur >= len(t.chatUsers) {
		return event
	}

	selected := t.chatUsers[cur]
	switch {
	case event.Key() == tcell.KeyEnter:
		if selected.PubKey == "" {
			return nil
		}

		// Toggle DM target.
		if t.dmTargetPubKey == selected.PubKey {
			t.dmTargetPubKey = ""
			t.dmTargetNick = ""
			t.dmTargetChat = ""
			t.actionsChan <- client.UserAction{Type: "SET_DM_TARGET", Payload: ""}
		} else {
			t.dmTargetPubKey = selected.PubKey
			if selected.Nick != "" {
				t.dmTargetNick = selected.Nick
			} else {
				t.dmTargetNick = selected.ShortPubKey
			}
			chat := selected.Chat
			if chat == "" && len(t.views) > 0 && t.activeViewIndex >= 0 && t.activeViewIndex < len(t.views) {
				// Best-effort fallback for cases where user's chat isn't populated yet.
				// This ensures DM scope matches the currently visible chat scope.
				if !t.views[t.activeViewIndex].IsGroup {
					chat = t.views[t.activeViewIndex].Name
				}
			}
			t.actionsChan <- client.UserAction{
				Type:    "SET_DM_TARGET",
				Payload: selected.PubKey + "|" + chat,
			}
		}

		// Clear input; user types the DM message.
		t.input.SetText("")
		t.app.SetFocus(t.input)
		t.updateInputLabel()
		t.updateHints()
		return nil
	case event.Key() == tcell.KeyRune && event.Rune() == 'p':
		// Same behavior as Enter for convenience.
		if selected.PubKey == "" {
			return nil
		}

		if t.dmTargetPubKey == selected.PubKey {
			t.dmTargetPubKey = ""
			t.dmTargetNick = ""
			t.dmTargetChat = ""
			t.actionsChan <- client.UserAction{Type: "SET_DM_TARGET", Payload: ""}
		} else {
			t.dmTargetPubKey = selected.PubKey
			if selected.Nick != "" {
				t.dmTargetNick = selected.Nick
			} else {
				t.dmTargetNick = selected.ShortPubKey
			}
			chat := selected.Chat
			if chat == "" && len(t.views) > 0 && t.activeViewIndex >= 0 && t.activeViewIndex < len(t.views) {
				if !t.views[t.activeViewIndex].IsGroup {
					chat = t.views[t.activeViewIndex].Name
				}
			}
			t.actionsChan <- client.UserAction{
				Type:    "SET_DM_TARGET",
				Payload: selected.PubKey + "|" + chat,
			}
		}

		t.input.SetText("")
		t.app.SetFocus(t.input)
		t.updateInputLabel()
		t.updateHints()
		return nil
	}

	return event
}
