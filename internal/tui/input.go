package tui

import (
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/lessucettes/strchat-tui/internal/client"
)

const maxInputHistory = 100

// bell is event-loop-only feedback for rejected input, not relay/network errors.
// A held key or a paste at the length limit must not produce a stream of BELs.
func (t *tui) bell() {
	now := time.Now()
	if now.Sub(t.lastBell) < 500*time.Millisecond {
		return
	}
	if screen := t.screen.Load(); screen != nil {
		t.lastBell = now
		_ = (*screen).Beep()
	}
}

// setupHandlers wires all key handling. Handlers run on the tview event loop;
// they never block (see submit) and never call QueueUpdate (which would block
// against the loop they are already running on).
func (t *tui) setupHandlers() {
	t.setupInputHandlers()
	t.app.SetInputCapture(t.handleKey)
	t.chatList.SetChangedFunc(func(index int, mainText, secondaryText string, shortcut rune) {
		t.updateDetailsView()
	})
}

// Rebind the fresh editor after clear, without replacing its layout primitive.
func (t *tui) setupInputHandlers() {
	t.input.SetAutocompleteUseTags(false)
	t.input.SetAutocompleteFunc(t.handleAutocomplete)
	t.input.SetAcceptanceFunc(func(text string, _ rune) bool {
		if len(text) > client.MaxMsgLen && graphemeLen(text) > client.MaxMsgLen {
			t.bell()
			return false
		}
		return true
	})
	t.applyInputTheme()
	// Enter sends the message or runs the slash command.
	t.input.SetDoneFunc(func(key tcell.Key) {
		if key != tcell.KeyEnter {
			return
		}
		text := strings.TrimSpace(t.input.GetText())
		if text == "" {
			return
		}
		t.rememberInput(text)
		if strings.HasPrefix(text, "/") {
			t.handleCommand(text)
			t.input.SetText("")
			return
		}
		s := t.snapshot()
		action := client.UserAction{Type: "SEND_MESSAGE", Payload: text}
		if view := activeView(s); view != nil {
			action.View = view.Name
			if view.IsGroup {
				action.Chat = t.conversationChat(s)
				if strings.HasPrefix(text, "@") {
					result, ready := t.currentMessageTarget(s)
					if !ready || result.Error != "" {
						t.bell()
						if ready && result.Error != "" {
							t.applyLocalEvent(client.DisplayEvent{Type: "ERROR", Content: result.Error})
						}
						t.renderConversationTitles(s)
						return
					}
					action.ExpectedChat = result.Chat
				} else if action.Chat == "" {
					t.bell()
					return
				} else {
					action.ExpectedChat = action.Chat
				}
			}
		}
		if t.submit(action) {
			t.input.SetText("")
		}
		// Backpressure keeps the text in the field. History records the input
		// attempt, not a claim that the message was delivered.
	})

	// Nickname completion requests are best-effort: a dropped request only
	// means the completion menu does not refresh for this keystroke.
	t.input.SetChangedFunc(func(text string) {
		t.markDirty() // The destination title depends on the current draft.
		nick, complete := extractNickPrefix(text)
		if complete {
			t.lastNickQuery = ""
			return
		}
		if !complete && strings.Contains(text, "#") && t.lastNickQuery == "" {
			return
		}
		if nick != "" && nick != t.lastNickQuery {
			t.lastNickQuery = nick
			t.submit(client.UserAction{Type: "REQUEST_NICK_COMPLETION", Payload: nick})
		}
	})

	// Input history is session-local; returning past the newest entry restores
	// the draft saved when browsing began. Neither end wraps around.
	t.input.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyCtrlP:
			if t.historyOffset == len(t.inputHistory) {
				t.bell()
				return nil
			}
			if t.historyOffset == 0 {
				t.inputDraft = t.input.GetText()
			}
			t.historyOffset++
		case tcell.KeyCtrlN:
			if t.historyOffset == 0 {
				t.bell()
				return nil
			}
			t.historyOffset--
		default:
			return ev
		}
		if t.historyOffset == 0 {
			t.input.SetText(t.inputDraft)
		} else {
			t.input.SetText(t.inputHistory[len(t.inputHistory)-t.historyOffset])
		}
		return nil
	})

}

// handleKey is the global key handler: focus management, maximize toggles,
// quitting, and dispatch to the focused widget.
func (t *tui) handleKey(event *tcell.EventKey) *tcell.EventKey {
	if event.Key() == wakeKey {
		return nil // internal repaint request; the following draw does the work
	}
	// Handle shutdown before pane dispatch, including empty lists and maximized
	// views. Consume Ctrl+C too: otherwise tview's default handler still quits.
	switch event.Key() {
	case tcell.KeyCtrlD:
		t.quit()
		return nil
	case tcell.KeyCtrlC:
		t.bell()
		return nil
	}
	t.syncFocus()

	if t.maximizeModeOf() != maximizeNone {
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
			t.focusIfVisible(t.chatList)
		case 'o':
			if t.infoTemporary {
				t.setFocus(t.input)
			}
			t.focusIfVisible(t.outputView)
		case 'i':
			t.focusIfVisible(t.input)
		case 'l':
			t.focusIfVisible(t.logsView)
		case 'n':
			t.openInfo()
		}
		return nil
	}
	switch {
	case t.focused == t.chatList:
		return t.handleChatListKeys(event)
	case t.focused == t.detailsView:
		if t.handleInfoKeys(event) == nil {
			return nil
		}
	case t.canMaximize() && t.focused == t.logsView && event.Key() == tcell.KeyRune && event.Rune() == '`':
		t.setMaximized(maximizeLogs)
		return nil
	case t.canMaximize() && t.focused == t.outputView && event.Key() == tcell.KeyRune && event.Rune() == '`':
		t.setMaximized(maximizeMessages)
		return nil
	}
	if t.focused != t.input && event.Key() == tcell.KeyRune {
		if strings.ContainsRune("gGhjkl", event.Rune()) {
			return event // Keep tview's native TextView scrolling shortcuts.
		}
		t.bell() // Text belongs in the input field, not a read-only pane.
		return nil
	}
	// Fall back to our own focus target when the layout hid tview's.
	if t.focused != nil && t.app.GetFocus() != t.focused {
		t.app.SetFocus(t.focused)
	}
	return event
}

// quit stops the UI, telling the client best-effort. The UI never waits for the
// client: Run's cleanup and main's client.Stop own the shutdown.
func (t *tui) quit() {
	t.submit(client.UserAction{Type: "QUIT"})
	t.requestQuit()
}

// handleMaximizedViewKeys handles keys while a log or message view is maximized.
func (t *tui) handleMaximizedViewKeys(event *tcell.EventKey) *tcell.EventKey {
	switch event.Key() {
	case tcell.KeyRune:
		if strings.ContainsRune("gGhjkl", event.Rune()) {
			return event
		}
		if event.Rune() == '`' {
			t.setMaximized(maximizeNone)
		} else {
			t.bell()
		}
		return nil
	case tcell.KeyTab, tcell.KeyBacktab:
		t.bell() // Restore the pane before trying to switch focus.
		return nil
	case tcell.KeyUp, tcell.KeyDown, tcell.KeyPgUp, tcell.KeyPgDn, tcell.KeyHome, tcell.KeyEnd:
		return event
	}
	return nil
}

// handleChatListKeys handles selection/activation in the chat list. The local
// multi-selection (space) and group creation (enter) are unchanged; a failed
// submission keeps the selection so the user can retry.
func (t *tui) handleChatListKeys(event *tcell.EventKey) *tcell.EventKey {
	switch event.Key() {
	case tcell.KeyUp, tcell.KeyDown, tcell.KeyHome, tcell.KeyEnd:
		return event
	}

	s := t.snapshot()
	count := t.chatList.GetItemCount()
	if count == 0 || len(s.views) == 0 {
		if event.Key() == tcell.KeyEnter || event.Key() == tcell.KeyDelete || event.Key() == tcell.KeyRune {
			t.bell()
			return nil
		}
		return event
	}
	cur := t.chatList.GetCurrentItem()
	if cur < 0 || cur >= len(s.views) {
		return event
	}
	selected := s.views[cur]

	switch event.Key() {
	case tcell.KeyRune:
		if event.Rune() == ' ' && !selected.IsGroup {
			if t.selectedForGroup[selected.Name] {
				delete(t.selectedForGroup, selected.Name)
			} else {
				t.selectedForGroup[selected.Name] = true
			}
			t.selectionRev++
			t.renderChatList(t.snapshot())
		} else {
			t.bell()
		}
		return nil
	case tcell.KeyEnter:
		if len(t.selectedForGroup) > 1 {
			members := make([]string, 0, len(t.selectedForGroup))
			for name := range t.selectedForGroup {
				members = append(members, name)
			}
			sort.Strings(members)
			if !t.submit(client.UserAction{Type: "CREATE_GROUP", Payload: strings.Join(members, ",")}) {
				return nil
			}
		} else if !t.submit(client.UserAction{Type: "ACTIVATE_VIEW", Payload: selected.Name}) {
			return nil
		}
		t.selectedForGroup = make(map[string]bool)
		t.selectionRev++
		t.renderChatList(t.snapshot())
		return nil
	case tcell.KeyDelete:
		action := "LEAVE_CHAT"
		if selected.IsGroup {
			action = "DELETE_GROUP"
		}
		t.submit(client.UserAction{Type: action, Payload: selected.Name})
		return nil
	}
	return event
}

// handleCommand parses and dispatches slash commands.
func (t *tui) handleCommand(text string) {
	command, payload := splitCommand(text)
	switch command {
	case "/clear":
		if payload != "" {
			t.bell()
			t.applyLocalEvent(client.DisplayEvent{Type: "ERROR", Content: "Usage: /clear"})
			return
		}
		t.clearSession()
	case "/quit", "/q":
		t.submit(client.UserAction{Type: "QUIT"})
		t.requestQuit()
	case "/join", "/j":
		if payload != "" {
			t.submit(client.UserAction{Type: "JOIN_CHATS", Payload: payload})
		} else {
			t.bell()
			t.applyLocalEvent(client.DisplayEvent{Type: "ERROR", Content: "Usage: /join <chat> [chat...]"})
		}
	case "/pow", "/p":
		if payload == "" {
			payload = "0"
		}
		t.submit(client.UserAction{Type: "SET_POW", Payload: payload})
	case "/list", "/l":
		t.submit(client.UserAction{Type: "LIST_CHATS"})
	case "/set", "/s":
		args := strings.Fields(payload)
		switch len(args) {
		case 0:
			t.submit(client.UserAction{Type: "GET_ACTIVE_CHAT"})
		case 1:
			t.submit(client.UserAction{Type: "ACTIVATE_VIEW", Payload: args[0]})
		default:
			for _, view := range t.snapshot().views {
				if !view.IsGroup || view.Name != args[0] {
					continue
				}
				if len(args) != 2 {
					t.bell()
					t.applyLocalEvent(client.DisplayEvent{Type: "ERROR", Content: "Usage: /set <group> <member>"})
					return
				}
				if t.submit(client.UserAction{Type: "ACTIVATE_VIEW", Payload: args[0], Chat: args[1]}) {
					t.rememberConversation(args[0], args[1])
				}
				return
			}
			t.submit(client.UserAction{Type: "CREATE_GROUP", Payload: strings.Join(args, ",")})
		}
	case "/nick", "/n":
		t.submit(client.UserAction{Type: "SET_NICK", Payload: payload})
	case "/del", "/d":
		t.submit(client.UserAction{Type: "DELETE_VIEW", Payload: payload})
	case "/block", "/b":
		if payload == "" {
			t.submit(client.UserAction{Type: "LIST_BLOCKED"})
		} else {
			t.submit(client.UserAction{Type: "BLOCK_USER", Payload: payload})
		}
	case "/unblock", "/ub":
		if payload == "" {
			t.submit(client.UserAction{Type: "LIST_BLOCKED"})
		} else {
			t.submit(client.UserAction{Type: "UNBLOCK_USER", Payload: payload})
		}
	case "/filter", "/f":
		t.submit(client.UserAction{Type: "HANDLE_FILTER", Payload: payload})
	case "/unfilter", "/uf":
		if payload == "" {
			t.submit(client.UserAction{Type: "CLEAR_FILTERS"})
		} else {
			t.submit(client.UserAction{Type: "REMOVE_FILTER", Payload: payload})
		}
	case "/mute", "/m":
		t.submit(client.UserAction{Type: "HANDLE_MUTE", Payload: payload})
	case "/unmute", "/um":
		if payload == "" {
			t.submit(client.UserAction{Type: "CLEAR_MUTES"})
		} else {
			t.submit(client.UserAction{Type: "REMOVE_MUTE", Payload: payload})
		}
	case "/relay", "/r":
		t.submit(client.UserAction{Type: "MANAGE_ANCHORS", Payload: payload})
	case "/help", "/h":
		t.submit(client.UserAction{Type: "GET_HELP", Payload: payload})
	case "/theme", "/t":
		t.handleTheme(payload)
	case "/scrollbar", "/sb":
		t.submit(client.UserAction{Type: "SET_SCROLLBAR", Payload: payload})
	default:
		t.bell()
		t.applyLocalEvent(client.DisplayEvent{Type: "ERROR", Content: "Unknown command: " + command + ". Use /help to list commands."})
	}
}

// splitCommand separates a slash command from its arguments using any
// whitespace, matching the input's outer TrimSpace before dispatch.
func splitCommand(text string) (command, payload string) {
	text = strings.TrimSpace(text)
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", ""
	}
	command = fields[0]
	payload = strings.TrimSpace(text[len(command):])
	return command, payload
}

// handleAutocomplete provides completion entries for the input field.
func (t *tui) handleAutocomplete(currentText string) []string {
	command, payload := splitCommand(currentText)
	switch command {
	case "/block", "/unblock", "/b", "/ub":
		if payload == "" {
			return nil
		}
		if len(t.completionEntries) == 0 {
			return nil
		}
		out := make([]string, 0, len(t.completionEntries))
		for _, e := range t.completionEntries {
			out = append(out, command+" "+e)
		}
		return out
	}

	nick, complete := extractNickPrefix(currentText)
	if complete {
		t.completionEntries = nil
		return nil
	}
	if nick == "" || len(t.completionEntries) == 0 {
		return nil
	}
	return append([]string(nil), t.completionEntries...)
}

// rememberInput retains submitted messages and commands on the event loop only.
// It never writes history to disk.
func (t *tui) rememberInput(text string) {
	if len(t.inputHistory) == maxInputHistory {
		copy(t.inputHistory, t.inputHistory[1:])
		t.inputHistory[len(t.inputHistory)-1] = text
	} else {
		t.inputHistory = append(t.inputHistory, text)
	}
	t.historyOffset = 0
	t.inputDraft = ""
}
