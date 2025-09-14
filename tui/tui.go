// tui/tui.go
package tui

import (
	"fmt"
	"log"
	"sort"
	"strchat-tui/client"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// TUI is the main struct that holds all TUI components.
type TUI struct {
	app         *tview.Application
	actionsChan chan<- client.UserAction

	mainFlex            *tview.Flex
	chatList            *tview.List
	detailsView         *tview.TextView
	logs                *tview.TextView
	output              *tview.TextView
	maximizedOutputFlex *tview.Flex
	outputMaximized     bool
	input               *tview.InputField
	hints               *tview.TextView
	maximizedLogsFlex   *tview.Flex
	logsMaximized       bool
	views               []client.View
	relays              []client.RelayInfo
	selectedForGroup    map[string]bool
	activeViewIndex     int
	completionEntries   []string
	nick                string
	recentRecipients    []string
	rrIdx               int

	// acPrefix keeps everything before the last '@' during autocomplete.
	acPrefix string
}

// New creates and initializes the entire TUI application.
func New(actions chan<- client.UserAction, events <-chan client.DisplayEvent) *TUI {
	t := &TUI{
		app:               tview.NewApplication(),
		actionsChan:       actions,
		logsMaximized:     false,
		outputMaximized:   false,
		selectedForGroup:  make(map[string]bool),
		activeViewIndex:   0,
		views:             []client.View{},
		relays:            []client.RelayInfo{},
		completionEntries: []string{},
		recentRecipients:  []string{},
		rrIdx:             -1,
		acPrefix:          "",
	}

	t.setupViews()
	t.setupHandlers()
	t.updateInputLabel()
	t.app.SetRoot(t.mainFlex, true).SetFocus(t.input)
	t.updateFocusBorders()
	t.updateHints()
	t.updateDetailsView()

	go t.listenForEvents(events)

	return t
}

// setupViews creates and configures all the visual primitives of the TUI.
func (t *TUI) setupViews() {
	tview.Styles.PrimitiveBackgroundColor = tcell.ColorBlack
	tview.Styles.PrimaryTextColor = tcell.ColorGainsboro
	tview.Styles.BorderColor = tcell.ColorDarkOliveGreen
	tview.Styles.TitleColor = tcell.ColorLimeGreen

	t.logs = tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetChangedFunc(func() { t.app.Draw() })
	t.logs.SetBorder(true).SetTitle("Logs (Alt+L)").SetTitleAlign(tview.AlignLeft)
	log.SetOutput(tview.ANSIWriter(t.logs))
	log.SetFlags(0)

	t.chatList = tview.NewList().
		ShowSecondaryText(false).
		SetSelectedBackgroundColor(tcell.ColorDarkOliveGreen)
	t.chatList.SetBorder(true).SetTitle("Chats (Alt+C)").SetTitleAlign(tview.AlignLeft)

	t.detailsView = tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetChangedFunc(func() { t.app.Draw() })
	t.detailsView.SetBorder(true).SetTitle("Info (Alt+N)").SetTitleAlign(tview.AlignLeft)

	sidebarFlex := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(t.chatList, 0, 1, true).
		AddItem(t.detailsView, 0, 1, false)

	t.output = tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetChangedFunc(func() { t.app.Draw() })
	t.output.SetBorder(true).SetTitle("Messages (Alt+O)").SetTitleAlign(tview.AlignLeft)

	t.input = tview.NewInputField().
		SetLabelStyle(tcell.StyleDefault.Foreground(tcell.ColorLimeGreen)).
		SetFieldBackgroundColor(tcell.NewRGBColor(0, 40, 0)).
		SetFieldTextColor(tcell.ColorLime)

	t.input.SetBorder(true).SetTitle("Input (Alt+I)").SetTitleAlign(tview.AlignLeft)

	t.input.SetAutocompleteFunc(t.handleAutocomplete)

	t.input.SetAcceptanceFunc(func(textToCheck string, lastChar rune) bool {
		return utf8.RuneCountInString(textToCheck) <= client.MaxMsgLen
	})

	t.hints = tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignLeft)

	contentGrid := tview.NewGrid().
		SetColumns(0, 30).
		SetBorders(false)

	contentGrid.AddItem(t.output, 0, 0, 1, 2, 0, 0, false)
	contentGrid.AddItem(t.output, 0, 0, 1, 1, 0, 100, false)
	contentGrid.AddItem(sidebarFlex, 0, 1, 1, 1, 0, 100, false)

	bottomFlex := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(t.input, 0, 1, true).
		AddItem(t.hints, 1, 0, false)

	t.mainFlex = tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(t.logs, 4, 0, false).
		AddItem(contentGrid, 0, 1, false).
		AddItem(bottomFlex, 4, 0, true)

	t.maximizedLogsFlex = tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(t.logs, 0, 1, true).
		AddItem(t.hints, 1, 0, false)

	t.maximizedOutputFlex = tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(t.output, 0, 1, true).
		AddItem(t.hints, 1, 0, false)
}

func (t *TUI) handleAutocomplete(currentText string) []string {
	trimmed := strings.TrimSpace(currentText)

	if strings.HasPrefix(trimmed, "/block ") ||
		strings.HasPrefix(trimmed, "/unblock ") ||
		strings.HasPrefix(trimmed, "/b ") ||
		strings.HasPrefix(trimmed, "/ub ") {

		parts := strings.SplitN(currentText, " ", 2)
		if len(parts) < 2 {
			return nil
		}
		cmd := parts[0] + " "
		nickPrefix := parts[1]

		t.acPrefix = cmd
		t.actionsChan <- client.UserAction{Type: "REQUEST_NICK_COMPLETION", Payload: nickPrefix}

		if len(t.completionEntries) == 0 {
			return nil
		}
		out := make([]string, 0, len(t.completionEntries))
		for _, e := range t.completionEntries {
			out = append(out, t.acPrefix+e)
		}
		return out
	}

	if !strings.Contains(currentText, "@") {
		t.acPrefix = ""
		t.completionEntries = nil
		return nil
	}

	lastAt := strings.LastIndex(currentText, "@")
	textAfterAt := currentText[lastAt:]

	if spaceIndex := strings.Index(textAfterAt, " "); spaceIndex != -1 &&
		len(strings.TrimSpace(textAfterAt[spaceIndex:])) > 0 {
		t.acPrefix = ""
		t.completionEntries = nil
		return nil
	}

	var nickPrefix string
	if spaceIndex := strings.Index(textAfterAt, " "); spaceIndex != -1 {
		nickPrefix = textAfterAt[1:spaceIndex]
	} else {
		nickPrefix = textAfterAt[1:]
	}

	t.acPrefix = currentText[:lastAt]
	t.actionsChan <- client.UserAction{Type: "REQUEST_NICK_COMPLETION", Payload: nickPrefix}

	return t.completionEntries
}

func (t *TUI) setupHandlers() {
	t.chatList.SetChangedFunc(func(index int, mainText string, secondaryText string, shortcut rune) {
		t.updateDetailsView()
	})

	t.input.SetDoneFunc(func(key tcell.Key) {
		if key != tcell.KeyEnter {
			return
		}
		text := strings.TrimSpace(t.input.GetText())
		if text == "" {
			return
		}

		if strings.HasPrefix(text, "/") {
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
			case "/help", "/h":
				t.actionsChan <- client.UserAction{Type: "GET_HELP"}
			}
		} else {
			t.actionsChan <- client.UserAction{Type: "SEND_MESSAGE", Payload: text}
		}

		if !strings.HasPrefix(text, "/") {
			s := strings.TrimSpace(text)
			if strings.HasPrefix(s, "@") {
				nick := s[1:]
				if i := strings.IndexByte(nick, ' '); i >= 0 {
					nick = nick[:i]
				}
				nick = strings.TrimSpace(nick)
				if nick != "" {
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
		}
		t.input.SetText("")
	})

	t.input.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyUp, tcell.KeyDown:
			if len(t.completionEntries) > 0 {
				return ev
			}

			txt := t.input.GetText()
			trim := strings.TrimSpace(txt)

			if len(t.recentRecipients) == 0 {
				return ev
			}

			canCycle := false
			startIdx := -1
			if trim == "" || trim == "@" {
				canCycle = true
			} else if strings.HasSuffix(txt, " ") &&
				strings.Count(strings.TrimSpace(txt), " ") == 0 &&
				strings.HasPrefix(trim, "@") {
				nick := trim[1:]
				for i, n := range t.recentRecipients {
					if n == nick {
						canCycle, startIdx = true, i
						break
					}
				}
			}
			if !canCycle {
				return ev
			}

			if t.rrIdx < 0 {
				if startIdx >= 0 {
					t.rrIdx = startIdx
				} else if ev.Key() == tcell.KeyUp {
					t.rrIdx = 0
				} else {
					t.rrIdx = len(t.recentRecipients) - 1
				}
			} else {
				if ev.Key() == tcell.KeyUp {
					t.rrIdx = (t.rrIdx + 1) % len(t.recentRecipients)
				} else {
					t.rrIdx = (t.rrIdx - 1 + len(t.recentRecipients)) % len(t.recentRecipients)
				}
			}

			t.input.SetText("@" + t.recentRecipients[t.rrIdx] + " ")
			return nil

		default:
			t.rrIdx = -1
			return ev
		}
	})

	t.app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if t.logsMaximized || t.outputMaximized {
			return t.handleMaximizedViewKeys(event)
		}

		if (event.Key() == tcell.KeyTab || event.Key() == tcell.KeyBacktab) && t.app.GetFocus() == t.input {
			if len(t.completionEntries) > 0 {
				return event
			}
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
}

func (t *TUI) cycleFocus(forward bool) {
	primitives := []tview.Primitive{t.input, t.chatList, t.output, t.logs, t.detailsView}
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

func (t *TUI) handleMaximizedViewKeys(event *tcell.EventKey) *tcell.EventKey {
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

func (t *TUI) handleChatListKeys(event *tcell.EventKey) *tcell.EventKey {
	if key := event.Key(); key == tcell.KeyUp || key == tcell.KeyDown || key == tcell.KeyHome || key == tcell.KeyEnd {
		return event
	}

	count := t.chatList.GetItemCount()
	if count == 0 || len(t.views) == 0 {
		return event
	}

	cur := t.chatList.GetCurrentItem()
	if cur < 0 || cur >= len(t.views) {
		return event
	}

	selectedView := t.views[cur]
	switch event.Key() {
	case tcell.KeyRune:
		if event.Rune() == ' ' {
			if !selectedView.IsGroup {
				if t.selectedForGroup[selectedView.Name] {
					delete(t.selectedForGroup, selectedView.Name)
				} else {
					t.selectedForGroup[selectedView.Name] = true
				}
				t.updateChatList()
			}
			return nil
		}
	case tcell.KeyEnter:
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
	case tcell.KeyDelete:
		action := "LEAVE_CHAT"
		if selectedView.IsGroup {
			action = "DELETE_GROUP"
		}
		t.actionsChan <- client.UserAction{Type: action, Payload: selectedView.Name}
		return nil
	}
	return event
}

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

func (t *TUI) listenForEvents(events <-chan client.DisplayEvent) {
	for event := range events {
		if event.Type == "SHUTDOWN" {
			break
		}

		t.app.QueueUpdateDraw(func() {
			switch event.Type {
			case "NEW_MESSAGE":

				if len(t.views) == 0 || t.activeViewIndex < 0 || t.activeViewIndex >= len(t.views) {
					return
				}

				activeView := t.views[t.activeViewIndex]
				showMessage := false
				if activeView.IsGroup {
					for _, childChat := range activeView.Children {
						if event.Chat == childChat {
							showMessage = true
							break
						}
					}
				} else {
					if event.Chat == activeView.Name {
						showMessage = true
					}
				}

				if showMessage {
					fmt.Fprintf(t.output, "[blue](%s)[-] <%s%s#%s[-]> [grey][%s %s][-]\n%s\n",
						event.Chat, event.Color, event.Nick, event.PubKey, event.ID, event.Timestamp, event.Content)
				}

				if !t.outputMaximized {
					t.output.ScrollToEnd()
				}
			case "INFO":
				fmt.Fprintf(t.output, "[blue]-- %s[-]\n", strings.TrimSpace(event.Content))
				if !t.outputMaximized {
					t.output.ScrollToEnd()
				}
			case "STATUS", "ERROR":
				color := "yellow"
				if event.Type == "ERROR" {
					color = "red"
				}
				fmt.Fprintf(t.logs, "[%s][%s] %s: %s[-]\n", color, time.Now().Format("15:04:05"), event.Type, event.Content)

				if !t.logsMaximized {
					t.logs.ScrollToEnd()
				}

			case "STATE_UPDATE":
				state, ok := event.Payload.(client.StateUpdate)
				if !ok {
					fmt.Fprintf(t.logs, "[red]ERROR: Invalid STATE_UPDATE payload[-]\n")
					return
				}
				t.views = state.Views
				t.activeViewIndex = state.ActiveViewIndex
				t.nick = state.Nick
				t.updateChatList()
				t.updateDetailsView()
				t.updateInputLabel()
			case "RELAYS_UPDATE":
				relays, ok := event.Payload.([]client.RelayInfo)
				if !ok {
					fmt.Fprintf(t.logs, "[red]ERROR: Invalid RELAYS_UPDATE payload[-]\n")
					return
				}
				t.relays = relays
				t.updateDetailsView()
			case "NICK_COMPLETION_RESULT":
				entries, ok := event.Payload.([]string)
				if !ok {
					return
				}
				if len(entries) == 0 && len(t.completionEntries) > 0 {
					return
				}
				t.completionEntries = entries
				t.input.Autocomplete()
			}
		})
	}
	t.app.Stop()
}

func (t *TUI) Run() error {
	return t.app.Run()
}

func (t *TUI) updateInputLabel() {
	if t.nick != "" {
		t.input.SetLabel(fmt.Sprintf("%s > ", t.nick))
	} else {
		t.input.SetLabel("> ")
	}
}

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
			hintText = "[lime]Enter[-]: Send | [lime]Tab/Shift+Tab[-]: Cycle Focus | " + baseHints
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
