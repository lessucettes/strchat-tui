package tui

import (
	"fmt"
	"io"
	"log"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/lessucettes/strchat-tui/internal/client"
)

// tui is the main struct that holds all tui components.
type tui struct {
	app         *tview.Application
	actionsChan chan<- client.UserAction

	// UI Components
	mainFlex            *tview.Flex
	chatList            *tview.List
	detailsView         *tview.TextView
	logs                *tview.TextView
	maximizedLogsFlex   *tview.Flex
	output              *tview.TextView
	maximizedOutputFlex *tview.Flex
	input               *tview.InputField
	hints               *tview.TextView

	// UI State
	logsMaximized   bool
	outputMaximized bool
	narrowMode      bool
	theme           *theme

	// App Data
	views            []client.View
	relays           []client.RelayInfo
	selectedForGroup map[string]bool
	activeViewIndex  int
	nick             string

	// Input-specific state
	completionEntries []string
	recentRecipients  []string
	rrIdx             int
	lastNickQuery     string
}

// New creates and initializes the entire TUI application.
func New(actions chan<- client.UserAction, events <-chan client.DisplayEvent) *tui {
	t := &tui{
		app:               tview.NewApplication(),
		actionsChan:       actions,
		logsMaximized:     false,
		outputMaximized:   false,
		views:             []client.View{},
		relays:            []client.RelayInfo{},
		selectedForGroup:  make(map[string]bool),
		activeViewIndex:   0,
		completionEntries: []string{},
		recentRecipients:  []string{},
		rrIdx:             -1,
		lastNickQuery:     "",
		theme:             defaultTheme,
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

// logWriter is a helper to redirect the standard logger to the logs TextView.
type logWriter struct {
	textViewWriter io.Writer
	getColor       func() tcell.Color
}

func (lw *logWriter) Write(p []byte) (int, error) {
	msg := strings.TrimSpace(string(p))
	ts := time.Now().Format("15:04:05")
	return fmt.Fprintf(lw.textViewWriter, "\n[%s][%s] %s[-]", lw.getColor(), ts, msg)
}

// Widget titles.
const (
	titleLogs     = "Logs (Alt+L)"
	titleChats    = "Chats (Alt+C)"
	titleInfo     = "Info (Alt+N)"
	titleMessages = "Messages (Alt+O)"
	titleInput    = "Input (Alt+I)"

	titleLogsShort     = "Alt+L"
	titleChatsShort    = "Alt+C"
	titleInfoShort     = "Alt+N"
	titleMessagesShort = "Alt+O"
	titleInputShort    = "Alt+I"
)

// setupViews creates and configures all the visual primitives of the TUI.
func (t *tui) setupViews() {
	t.applyTheme()
	t.initViews()
	t.initLayout()
}

// applyTheme sets the global styles for the application based on the current theme.
func (t *tui) applyTheme() {
	tview.Styles.PrimitiveBackgroundColor = t.theme.backgroundColor
	tview.Styles.PrimaryTextColor = t.theme.textColor
	tview.Styles.BorderColor = t.theme.borderColor
	tview.Styles.TitleColor = t.theme.titleColor
}

// initViews initializes all the individual widgets for the TUI.
func (t *tui) initViews() {
	t.logs = tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetChangedFunc(func() { t.app.Draw() })
	t.logs.SetBorder(true).SetTitle(titleLogs).SetTitleAlign(tview.AlignLeft)
	customWriter := &logWriter{
		textViewWriter: tview.ANSIWriter(t.logs),
		getColor:       func() tcell.Color { return t.theme.logInfoColor },
	}
	log.SetOutput(customWriter)
	log.SetFlags(0)

	t.chatList = tview.NewList().
		ShowSecondaryText(false).
		SetSelectedBackgroundColor(t.theme.borderColor)
	t.chatList.SetBorder(true).SetTitle(titleChats).SetTitleAlign(tview.AlignLeft)

	t.detailsView = tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetChangedFunc(func() { t.app.Draw() })
	t.detailsView.SetBorder(true).SetTitle(titleInfo).SetTitleAlign(tview.AlignLeft)

	t.output = tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetChangedFunc(func() { t.app.Draw() })
	t.output.SetBorder(true).SetTitle(titleMessages).SetTitleAlign(tview.AlignLeft)

	t.input = tview.NewInputField().
		SetLabelStyle(tcell.StyleDefault.Foreground(t.theme.titleColor)).
		SetFieldBackgroundColor(t.theme.inputBgColor).
		SetFieldTextColor(t.theme.inputTextColor)
	t.input.SetBorder(true).SetTitle(titleInput).SetTitleAlign(tview.AlignLeft)
	t.input.SetAutocompleteFunc(t.handleAutocomplete)
	t.input.SetAcceptanceFunc(func(textToCheck string, lastChar rune) bool {
		return utf8.RuneCountInString(textToCheck) <= client.MaxMsgLen
	})
	t.input.SetChangedFunc(func(text string) {
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
			t.actionsChan <- client.UserAction{
				Type:    "REQUEST_NICK_COMPLETION",
				Payload: nick,
			}
		}
	})

	t.hints = tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignLeft)
}

// initLayout composes the widgets into the final layout and sets up responsiveness.
func (t *tui) initLayout() {
	sidebarFlex := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(t.chatList, 0, 1, true).
		AddItem(t.detailsView, 0, 1, false)

	sidebarFlexHorizontal := tview.NewFlex().
		SetDirection(tview.FlexColumn).
		AddItem(t.chatList, 0, 1, true).
		AddItem(t.detailsView, 0, 1, false)

	contentGrid := tview.NewGrid().SetBorders(false)

	const narrowWidth = 100
	t.app.SetBeforeDrawFunc(func(screen tcell.Screen) bool {
		w, _ := screen.Size()
		contentGrid.Clear()

		if w < narrowWidth {
			if !t.narrowMode {
				t.narrowMode = true
				t.logs.SetTitle(titleLogsShort)
				t.output.SetTitle(titleMessagesShort)
				t.chatList.SetTitle(titleChatsShort)
				t.detailsView.SetTitle(titleInfoShort)
				t.input.SetTitle(titleInputShort)
				t.input.SetLabel("> ")
			}
			contentGrid.SetRows(0, 5)
			contentGrid.SetColumns(0)
			contentGrid.AddItem(t.output, 0, 0, 1, 1, 0, 0, false)
			contentGrid.AddItem(sidebarFlexHorizontal, 1, 0, 1, 1, 0, 0, false)
		} else {
			if t.narrowMode {
				t.narrowMode = false
				t.logs.SetTitle(titleLogs)
				t.output.SetTitle(titleMessages)
				t.chatList.SetTitle(titleChats)
				t.detailsView.SetTitle(titleInfo)
				t.input.SetTitle(titleInput)
				t.updateInputLabel()
			}
			contentGrid.SetRows(0)
			contentGrid.SetColumns(0, 30)
			contentGrid.AddItem(t.output, 0, 0, 1, 1, 0, 0, false)
			contentGrid.AddItem(sidebarFlex, 0, 1, 1, 1, 0, 0, false)
		}
		return false
	})

	bottomFlex := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(t.input, 0, 1, true).
		AddItem(t.hints, 1, 0, false)

	t.mainFlex = tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(t.logs, 3, 0, false).
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

// handleAutocomplete provides completion entries for the input field.
func (t *tui) handleAutocomplete(currentText string) []string {
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

		if len(t.completionEntries) == 0 {
			return nil
		}
		out := make([]string, 0, len(t.completionEntries))
		for _, e := range t.completionEntries {
			out = append(out, cmd+e)
		}
		return out
	}

	nick, complete := extractNickPrefix(currentText)
	if complete {
		t.completionEntries = nil
		return nil
	}
	if nick == "" {
		return nil
	}

	if len(t.completionEntries) == 0 {
		return nil
	}

	return append([]string(nil), t.completionEntries...)
}

// listenForEvents is the main event loop that processes events from the client.
func (t *tui) listenForEvents(events <-chan client.DisplayEvent) {
	for event := range events {
		if event.Type == "SHUTDOWN" {
			break
		}

		t.app.QueueUpdateDraw(func() {
			switch event.Type {
			case "NEW_MESSAGE":
				t.handleNewMessage(event)
			case "INFO":
				t.handleInfoMessage(event)
			case "STATUS", "ERROR":
				t.handleLogMessage(event)
			case "STATE_UPDATE":
				t.handleStateUpdate(event)
			case "RELAYS_UPDATE":
				t.handleRelaysUpdate(event)
			case "NICK_COMPLETION_RESULT":
				t.handleNickCompletion(event)
			}
		})
	}
	t.app.Stop()
}

// handleNewMessage processes and displays a new chat message.
func (t *tui) handleNewMessage(event client.DisplayEvent) {
	if len(t.views) == 0 || t.activeViewIndex < 0 || t.activeViewIndex >= len(t.views) {
		return
	}

	activeView := t.views[t.activeViewIndex]
	showMessage := false
	if activeView.IsGroup {
		if slices.Contains(activeView.Children, event.Chat) {
			showMessage = true
		}
	} else {
		if event.Chat == activeView.Name {
			showMessage = true
		}
	}

	if showMessage {
		nickColorTag := pubkeyToColor(event.FullPubKey, t.theme.nickPalette)

		ownColorTag := fmt.Sprintf("[%s]", t.theme.inputTextColor)
		ownNickTag := fmt.Sprintf("[%s::b]", t.theme.inputTextColor)

		mention := "@" + t.nick
		content := event.Content
		if t.nick != "" && strings.Contains(content, mention) {
			content = strings.ReplaceAll(
				content,
				mention,
				fmt.Sprintf("[%s::b]%s[-::-]", t.theme.inputTextColor, mention),
			)
		}

		label := ""
		activeView := t.views[t.activeViewIndex]
		if activeView.IsGroup {
			label = fmt.Sprintf("[%s]%s[-] ", t.theme.titleColor, event.Chat)
		}

		if event.IsOwnMessage {
			fmt.Fprintf(
				t.output,
				"\n%s%s%s[-::-]#%s> %s%s[-] [%s][%s %s][-]",
				label,
				ownNickTag, event.Nick, event.ShortPubKey,
				ownColorTag, content,
				t.theme.logInfoColor, event.ID, event.Timestamp,
			)
		} else {
			fmt.Fprintf(
				t.output,
				"\n%s%s%s[-::-]#%s> %s [%s][%s %s][-]",
				label,
				nickColorTag, event.Nick, event.ShortPubKey,
				content,
				t.theme.logInfoColor, event.ID, event.Timestamp,
			)
		}
	}
	if !t.outputMaximized {
		t.output.ScrollToEnd()
	}
}

// handleInfoMessage displays a generic informational message in the output view.
func (t *tui) handleInfoMessage(event client.DisplayEvent) {
	content := tview.Escape(strings.TrimSpace(event.Content))
	fmt.Fprintf(t.output, "\n[%s]-- %s[-]", t.theme.titleColor, content)
	if !t.outputMaximized {
		t.output.ScrollToEnd()
	}
}

// handleLogMessage displays a status or error message in the logs view.
func (t *tui) handleLogMessage(event client.DisplayEvent) {
	color := t.theme.logWarnColor
	if event.Type == "ERROR" {
		color = t.theme.logErrorColor
	}
	fmt.Fprintf(t.logs, "\n[%s][%s] %s: %s[-]", color, time.Now().Format("15:04:05"), event.Type, event.Content)
	if !t.logsMaximized {
		t.logs.ScrollToEnd()
	}
}

// handleStateUpdate updates the TUI's state based on data from the client.
func (t *tui) handleStateUpdate(event client.DisplayEvent) {
	state, ok := event.Payload.(client.StateUpdate)
	if !ok {
		fmt.Fprintf(t.logs, "\n[%s]ERROR: Invalid STATE_UPDATE payload[-]", t.theme.logErrorColor)
		return
	}
	t.views = state.Views
	t.activeViewIndex = state.ActiveViewIndex
	t.nick = state.Nick
	t.updateChatList()
	t.updateDetailsView()
	t.updateInputLabel()
}

// handleRelaysUpdate refreshes the list of relays.
func (t *tui) handleRelaysUpdate(event client.DisplayEvent) {
	relays, ok := event.Payload.([]client.RelayInfo)
	if !ok {
		fmt.Fprintf(t.logs, "\n[%s]ERROR: Invalid RELAYS_UPDATE payload[-]", t.theme.logErrorColor)
		return
	}
	t.relays = relays
	t.updateDetailsView()
}

// handleNickCompletion provides completion entries to the input field.
func (t *tui) handleNickCompletion(event client.DisplayEvent) {
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

// Run starts the TUI application.
func (t *tui) Run() error {
	return t.app.Run()
}
