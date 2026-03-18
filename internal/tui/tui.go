package tui

import (
	"fmt"
	"io"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

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
	userList            *tview.List
	detailsView         *tview.List
	relaysFooter        *tview.TextView
	relaysPanel         *tview.Flex
	logs                *tview.TextView
	maximizedLogsFlex   *tview.Flex
	output              *tview.List
	maximizedOutputFlex *tview.Flex
	input               *tview.InputField
	hints               *tview.TextView
	wideComposerFlex    *tview.Flex
	wideBottomFlex      *tview.Flex
	narrowComposerFlex  *tview.Flex

	// UI State

	logsMaximized   bool
	outputMaximized bool
	narrowMode      bool
	theme           *theme

	narrowFlex         *tview.Flex
	contentGrid        *tview.Grid // wide layout middle (messages + sidebar)

	// Current user's identity short prefix (for the active view).
	selfShortPubKey string

	// App Data

	views            []client.View
	relays           []client.RelayInfo
	relaysUpCount    int
	relaysDownCount  int
	selectedForGroup map[string]bool
	activeViewIndex  int
	nick             string

	chatUsers          []client.ChatUser
	chatUsersByPubKey map[string]client.ChatUser

	chatListItems []chatListItem

	outputMessages   []outputMessage
	outputRowToMsg   []int // list row index -> outputMessages index
	messagesCachedWrapW int

	userPruneStopCh chan struct{}

	// Resize/layout switching (debounced).
	resizeMu          sync.Mutex
	desiredNarrowMode bool
	resizeApplyTimer  *time.Timer

	// Input-specific state

	completionEntries []string
	recentRecipients  []string
	rrIdx             int
	lastNickQuery     string

	// Follow means: when enabled, always keep the message list scrolled to newest.
	followEnabled bool

	pendingReply *pendingReply

	// Distinct per-participant colors in the active chat (no duplicate hues among peers).
	participantColorTag map[string]string
	participantHue      map[string]float64
	chatColorMu         sync.RWMutex

	pullingStatus     string     // e.g. "#moscow"; shown in Info until messages arrive
	pullingStatusTimer *time.Timer
}

type chatListItem struct {
	viewIndex int
}

type outputMessage struct {
	Replyable    bool
	Nick         string
	ShortPubKey  string
	Content      string
	RawDisplay   string // full styled line(s) source for re-wrap on resize
}

// pendingReply holds the message being replied to (input title + send formatting).
type pendingReply struct {
	Nick        string
	ShortPubKey string
	Content     string
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
		chatUsers:         []client.ChatUser{},
		chatUsersByPubKey: make(map[string]client.ChatUser),
		completionEntries: []string{},
		recentRecipients:  []string{},
		rrIdx:             -1,
		lastNickQuery:     "",
		theme:             defaultTheme,
		userPruneStopCh:  make(chan struct{}),
		followEnabled:       true,
		participantColorTag: make(map[string]string),
		participantHue:      make(map[string]float64),
	}

	t.setupViews()
	t.setupHandlers()
	t.updateInputLabel()
	t.app.SetRoot(t.mainFlex, true).SetFocus(t.input)
	t.updateFocusBorders()
	t.updateHints()
	t.updateDetailsView()

	go t.listenForEvents(events)
	go t.userPruner()

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
	titleLogs     = "LOGS (Alt+L)"
	titleChats    = "CHATS (Alt+C)"
	titleUsers    = "USERS (Alt+U) ONLINE:"
	titleRelays   = "RELAYS (Alt+R)"
	titleMessages = "MESSAGES (Alt+M)"
	titleInput    = "INPUT (Alt+I)"

	titleLogsShort     = "LOGS"
	titleChatsShort    = "CHATS"
	titleUsersShort    = "USERS"
	titleRelaysShort   = "RELAYS"
	titleMessagesShort = "MESSAGES"
	titleInputShort    = "INPUT"
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

func (t *tui) followTitleSuffix() string {
	if t.followEnabled {
		return " | FOLLOW: ON"
	}
	return " | FOLLOW: OFF"
}

func (t *tui) refreshFollowTitle() {
	// FOLLOW status must always be shown on the Messages window.
	if t.narrowMode {
		t.output.SetTitle(titleMessagesShort + t.followTitleSuffix())
	} else {
		t.output.SetTitle(titleMessages + t.followTitleSuffix())
	}
}

// jumpToLastMessage moves selection to the newest message (used when FOLLOW turns ON).
func (t *tui) jumpToLastMessage() {
	if t.output == nil {
		return
	}
	n := t.output.GetItemCount()
	if n > 0 {
		t.output.SetCurrentItem(n - 1)
	}
}

// initViews initializes all the individual widgets for the TUI.
func (t *tui) initViews() {
	t.logs = tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetWrap(false).
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
		SetSelectedBackgroundColor(t.theme.borderColor).
		SetSelectedTextColor(t.theme.listSelectedFg)
	t.chatList.SetBorder(true).SetTitle(titleChats).SetTitleAlign(tview.AlignLeft)

	t.userList = tview.NewList().
		ShowSecondaryText(false).
		SetSelectedBackgroundColor(t.theme.borderColor).
		SetSelectedTextColor(t.theme.listSelectedFg)
	t.userList.SetBorder(true).SetTitleAlign(tview.AlignLeft)
	t.refreshUserListTitle()

	t.detailsView = tview.NewList().
		ShowSecondaryText(false).
		SetSelectedBackgroundColor(t.theme.borderColor).
		SetSelectedTextColor(t.theme.listSelectedFg)
	t.relaysFooter = tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignLeft).
		SetWrap(false)
	t.relaysPanel = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(t.detailsView, 0, 1, true).
		AddItem(t.relaysFooter, 1, 0, false)
	t.relaysPanel.SetBorder(true).SetTitle(titleRelays).SetTitleAlign(tview.AlignLeft)
	t.updateRelaysFooter()

	t.output = tview.NewList().
		ShowSecondaryText(false).
		// Hide selection highlight when messages are not focused.
		SetSelectedBackgroundColor(t.theme.backgroundColor).
		SetSelectedTextColor(t.theme.textColor).
		SetMainTextColor(t.theme.textColor).
		SetHighlightFullLine(false)
	t.output.SetBorder(true).SetTitle(titleMessages+t.followTitleSuffix()).SetTitleAlign(tview.AlignLeft)

	t.input = tview.NewInputField().
		SetLabelStyle(tcell.StyleDefault.Foreground(t.theme.titleColor)).
		SetFieldBackgroundColor(t.theme.inputBgColor).
		SetFieldTextColor(t.theme.inputTextColor)
	t.input.SetBorder(true).SetTitle(titleInput).SetTitleAlign(tview.AlignLeft)
	t.input.SetAutocompleteFunc(t.handleAutocomplete)
	t.input.SetAcceptanceFunc(func(textToCheck string, lastChar rune) bool {
		return graphemeLen(textToCheck) <= client.MaxMsgLen
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
		AddItem(t.userList, 0, 1, false).
		AddItem(t.relaysPanel, 0, 2, false)

	t.contentGrid = tview.NewGrid().SetBorders(false)
	contentGrid := t.contentGrid

	t.wideComposerFlex = tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(t.input, 0, 1, true)
	t.wideBottomFlex = tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(t.wideComposerFlex, 0, 1, true).
		AddItem(t.hints, 1, 0, false)

	t.narrowComposerFlex = tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(t.input, 0, 1, true)
	t.narrowFlex = tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(t.output, 0, 1, false).
		AddItem(t.narrowComposerFlex, 1, 0, false)

	const narrowWidth = 100
	t.app.SetBeforeDrawFunc(func(screen tcell.Screen) bool {
		w, _ := screen.Size()
		contentGrid.Clear()

		desiredNarrow := w < narrowWidth

		// Debounce root switching so it doesn't happen repeatedly during a resize drag.
		t.resizeMu.Lock()
		t.desiredNarrowMode = desiredNarrow
		if desiredNarrow != t.narrowMode {
			if t.resizeApplyTimer != nil {
				t.resizeApplyTimer.Stop()
			}
			t.resizeApplyTimer = time.AfterFunc(150*time.Millisecond, func() {
				t.resizeMu.Lock()
				narrow := t.desiredNarrowMode
				t.resizeMu.Unlock()

				// Apply mode switch on UI goroutine.
				t.app.QueueUpdateDraw(func() {
					if narrow == t.narrowMode {
						return
					}
					if narrow {
						t.narrowMode = true
						t.logs.SetTitle(titleLogsShort).SetTitleAlign(tview.AlignLeft)
						t.output.SetTitle(titleMessagesShort + t.followTitleSuffix())
						t.chatList.SetTitle(titleChatsShort)
						t.refreshUserListTitle()
						t.updateDetailsView()
						t.updateInputLabel()
						t.restoreNarrowComposerNormal()
						t.app.SetRoot(t.narrowFlex, true).SetFocus(t.input)
					} else {
						t.narrowMode = false
						t.logs.SetTitle(titleLogs).SetTitleAlign(tview.AlignLeft)
						t.output.SetTitle(titleMessages + t.followTitleSuffix())
						t.chatList.SetTitle(titleChats)
						t.refreshUserListTitle()
						t.updateDetailsView()
						t.input.SetTitle(titleInput)
						t.updateInputLabel()
						t.restoreWideComposerNormal()
						t.app.SetRoot(t.mainFlex, true)
					}
				})
			})
		}
		t.resizeMu.Unlock()

		nw := t.messagesWrapColumns(w)
		if nw != t.messagesCachedWrapW {
			t.messagesCachedWrapW = nw
			if len(t.outputMessages) > 0 {
				t.rebuildMessagesOutputPreservingSelection()
			}
		}

		// Configure grid only for wide mode; in narrow mode it's hidden by a different root.
		if !desiredNarrow {
			contentGrid.SetRows(0)
			// Fixed width of the right sidebar: chat list + users + details.
			// Keep it small enough so "Nick#hash" stays on one line, while
			// giving more horizontal room to the Messages area.
			contentGrid.SetColumns(0, 30)
			contentGrid.AddItem(t.output, 0, 0, 1, 1, 0, 0, false)
			contentGrid.AddItem(sidebarFlex, 0, 1, 1, 1, 0, 0, false)
		}
		return false
	})

	t.maximizedLogsFlex = tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(t.logs, 0, 1, true).
		AddItem(t.hints, 1, 0, false)

	t.maximizedOutputFlex = tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(t.output, 0, 1, true).
		AddItem(t.hints, 1, 0, false)

	t.mainFlex = tview.NewFlex().SetDirection(tview.FlexRow)
	t.rebuildMainFlexBottom(wideBottomRowsNormal)
}

const wideBottomRowsNormal = 4

func (t *tui) formatContentWithNickMentions(content string) string {
	if len(t.chatUsersByPubKey) == 0 {
		return content
	}
	lookup := make(map[string]string, len(t.chatUsersByPubKey)*2)
	for pk, u := range t.chatUsersByPubKey {
		if u.Nick == "" || u.ShortPubKey == "" {
			continue
		}
		sh := strings.ToLower(u.ShortPubKey)
		if len(sh) > 4 {
			sh = sh[len(sh)-4:]
		}
		lookup[strings.ToLower(u.Nick)+"#"+sh] = pk
	}
	idxs := nickHashMentionIndices(content)
	if len(idxs) == 0 {
		return content
	}
	var b strings.Builder
	prev := 0
	for _, loc := range idxs {
		fullStart, fullEnd := loc[0], loc[1]
		nick := strings.ToLower(content[loc[2]:loc[3]])
		hsh := strings.ToLower(content[loc[4]:loc[5]])
		key := nick + "#" + hsh
		b.WriteString(content[prev:fullStart])
		if pk, ok := lookup[key]; ok {
			b.WriteString(t.colorTagForPubkey(pk))
			b.WriteString(content[fullStart:fullEnd])
			b.WriteString("[-]")
		} else {
			b.WriteString(content[fullStart:fullEnd])
		}
		prev = fullEnd
	}
	b.WriteString(content[prev:])
	return b.String()
}

// rebuildMainFlexBottom sets how many terminal rows the wide-mode input strip uses.
func (t *tui) rebuildMainFlexBottom(bottomFixed int) {
	if t.mainFlex == nil || t.contentGrid == nil {
		return
	}
	t.mainFlex.Clear()
	t.mainFlex.AddItem(t.logs, 3, 0, false).
		AddItem(t.contentGrid, 0, 1, false).
		AddItem(t.wideBottomFlex, bottomFixed, 0, true)
}

// restoreWideComposerNormal puts single-line input back in the wide bottom area.
func (t *tui) restoreWideComposerNormal() {
	t.wideComposerFlex.Clear()
	t.wideComposerFlex.AddItem(t.input, 0, 1, true)
	t.wideBottomFlex.Clear()
	t.wideBottomFlex.AddItem(t.wideComposerFlex, 0, 1, true).AddItem(t.hints, 1, 0, false)
	t.rebuildMainFlexBottom(wideBottomRowsNormal)
}

// restoreNarrowComposerNormal: chat + single-line input (narrow).
func (t *tui) restoreNarrowComposerNormal() {
	t.narrowFlex.Clear()
	t.narrowFlex.AddItem(t.output, 0, 1, false)
	t.narrowComposerFlex.Clear()
	t.narrowComposerFlex.AddItem(t.input, 0, 1, true)
	t.narrowFlex.AddItem(t.narrowComposerFlex, 1, 0, false)
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
			// Stop background goroutines.
			select {
			case <-t.userPruneStopCh:
				// already closed
			default:
				close(t.userPruneStopCh)
			}
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
			case "CHAT_USERS_UPDATE":
				t.handleChatUsersUpdate(event)
			case "CHAT_USER_DISCOVERED":
				t.handleChatUserDiscovered(event)
			}
		})
	}
	t.app.Stop()
}

func (t *tui) userPruner() {
	// Periodically drop users whose last message is older than 3 minutes.
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			now := time.Now()
			t.app.QueueUpdateDraw(func() {
				t.pruneStaleChatUsers(now)
			})
		case <-t.userPruneStopCh:
			return
		}
	}
}

func (t *tui) pruneStaleChatUsers(now time.Time) {
	if t.chatUsersByPubKey == nil || len(t.chatUsers) == 0 {
		return
	}

	cutoff := now.Add(-3 * time.Minute)
	newUsers := make([]client.ChatUser, 0, len(t.chatUsers))
	newByPubKey := make(map[string]client.ChatUser, len(t.chatUsersByPubKey))

	for _, u := range t.chatUsers {
		if u.PubKey == "" {
			continue
		}
		if u.LastMsgAt <= 0 {
			continue
		}
		last := time.Unix(u.LastMsgAt, 0)
		if last.Before(cutoff) {
			continue
		}
		newUsers = append(newUsers, u)
		newByPubKey[u.PubKey] = u
	}

	// Avoid churn: if nothing changed, don't redraw.
	if len(newUsers) == len(t.chatUsers) {
		t.chatUsersByPubKey = newByPubKey
		t.rebuildParticipantColorsFull()
		return
	}

	t.chatUsers = newUsers
	t.chatUsersByPubKey = newByPubKey
	t.rebuildParticipantColorsFull()
	t.updateUserList()
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
	if !showMessage {
		return
	}

	clearedPulling := false
	if t.pullingStatus != "" {
		t.clearPullingMessagesStatus()
		clearedPulling = true
	}

	t.maybeUpsertChatUserFromEvent(event)

	// Smart follow: only select new message when user was already at bottom.
	itemCountBefore := t.output.GetItemCount()
	currentSelBefore := t.output.GetCurrentItem()
	shouldFollow := itemCountBefore == 0 || currentSelBefore == itemCountBefore-1
	if t.followEnabled {
		shouldFollow = true
	}

	nickColorTag := t.colorTagForPubkey(event.FullPubKey)
	ownColorTag := fmt.Sprintf("[%s]", t.theme.inputTextColor)
	ownNickTag := fmt.Sprintf("[%s::b]", t.theme.inputTextColor)

	label := ""
	if activeView.IsGroup {
		label = fmt.Sprintf("[%s]%s[-] ", t.theme.titleColor, event.Chat)
	}

	// Keep content single-line for list rendering.
	content := strings.ReplaceAll(event.Content, "\n", " ")
	mentionMe := !event.IsOwnMessage && isSelfMentionedInContent(content, t.nick, t.selfShortPubKey)

	// Stand out from normal lines; green tint matches terminal theme.
	const mentionBg = "#0d280d"
	const mentionFg = "#c8ffc8"
	const mentionMeta = "#6b9b6b"

	if t.nick != "" && !mentionMe {
		content = highlightPlainMentionOfMe(content, t.nick, t.theme.inputTextColor)
	}
	content = t.formatContentWithNickMentions(content)

	var display string
	if mentionMe {
		mc := strings.ReplaceAll(event.Content, "\n", " ")
		mc = t.formatContentWithNickMentions(mc)
		var b strings.Builder
		b.WriteString(fmt.Sprintf("[%s:%s:b]", mentionFg, mentionBg))
		if activeView.IsGroup {
			b.WriteString(event.Chat)
			b.WriteString(" ")
		}
		b.WriteString(event.Nick)
		b.WriteString("#")
		b.WriteString(event.ShortPubKey)
		b.WriteString("> ")
		b.WriteString(mc)
		b.WriteString(fmt.Sprintf(" [%s:%s]", mentionMeta, mentionBg))
		b.WriteString(event.ID)
		b.WriteString(" ")
		b.WriteString(event.Timestamp)
		b.WriteString("[-]")
		display = b.String()
	} else if event.IsOwnMessage {
		display = fmt.Sprintf(
			"%s%s%s[-]#%s> %s%s[-] [%s][%s %s][-]",
			label,
			ownNickTag, event.Nick,
			event.ShortPubKey,
			ownColorTag, content,
			t.theme.logInfoColor, event.ID, event.Timestamp,
		)
	} else {
		display = fmt.Sprintf(
			"%s%s%s[-]#%s> %s [%s][%s %s][-]",
			label,
			nickColorTag, event.Nick,
			event.ShortPubKey,
			content,
			t.theme.logInfoColor, event.ID, event.Timestamp,
		)
	}

	wrapW := t.messagesCachedWrapW
	if wrapW < 20 {
		wrapW = 76
	}
	lines := wrapTviewDisplay(display, wrapW)
	msgIdx := len(t.outputMessages)
	for _, ln := range lines {
		t.output.AddItem(ln, "", 0, nil)
		t.outputRowToMsg = append(t.outputRowToMsg, msgIdx)
	}
	t.outputMessages = append(t.outputMessages, outputMessage{
		Replyable:   true,
		Nick:        event.Nick,
		ShortPubKey: event.ShortPubKey,
		Content:     event.Content,
		RawDisplay:  display,
	})

	if shouldFollow {
		t.output.SetCurrentItem(t.output.GetItemCount() - 1)
	}
	if clearedPulling {
		t.updateDetailsView()
	}
}

func (t *tui) startPullingMessagesStatus() {
	if t.pullingStatusTimer != nil {
		t.pullingStatusTimer.Stop()
		t.pullingStatusTimer = nil
	}
	t.pullingStatus = ""
	if t.activeViewIndex < 0 || t.activeViewIndex >= len(t.views) {
		return
	}
	v := t.views[t.activeViewIndex]
	t.pullingStatus = "#" + strings.TrimPrefix(v.Name, "#")
	t.pullingStatusTimer = time.AfterFunc(12*time.Second, func() {
		t.app.QueueUpdateDraw(func() {
			t.clearPullingMessagesStatus()
			t.updateDetailsView()
		})
	})
}

func (t *tui) clearPullingMessagesStatus() {
	t.pullingStatus = ""
	if t.pullingStatusTimer != nil {
		t.pullingStatusTimer.Stop()
		t.pullingStatusTimer = nil
	}
}

// clearMessagesWindow clears the visible Messages list (local UI only).
func (t *tui) clearMessagesWindow() {
	if t.output == nil {
		return
	}
	t.output.Clear()
	t.outputMessages = nil
	t.outputRowToMsg = nil
	if t.output.GetItemCount() > 0 {
		t.output.SetCurrentItem(0)
	}
}

// handleInfoMessage displays a generic informational message in the output view.
func (t *tui) handleInfoMessage(event client.DisplayEvent) {
	if t.output == nil {
		return
	}
	content := strings.TrimSpace(event.Content)
	disp := fmt.Sprintf("-- %s", content)
	wrapW := t.messagesCachedWrapW
	if wrapW < 20 {
		wrapW = 76
	}
	msgIdx := len(t.outputMessages)
	for _, ln := range wrapTviewDisplay(disp, wrapW) {
		t.output.AddItem(ln, "", 0, nil)
		t.outputRowToMsg = append(t.outputRowToMsg, msgIdx)
	}
	t.outputMessages = append(t.outputMessages, outputMessage{
		Replyable:  false,
		Content:    content,
		RawDisplay: disp,
	})
}

// replyToSelectedMessage starts reply mode: title shows target; send prepends quote block.
func (t *tui) replyToSelectedMessage() {
	if t.output == nil || t.outputMessages == nil {
		return
	}
	idx := t.output.GetCurrentItem()
	if idx < 0 || idx >= len(t.outputRowToMsg) {
		return
	}
	mid := t.outputRowToMsg[idx]
	if mid < 0 || mid >= len(t.outputMessages) {
		return
	}
	m := t.outputMessages[mid]
	if !m.Replyable {
		return
	}

	go func() {
		t.app.QueueUpdateDraw(func() {
			t.pendingReply = &pendingReply{
				Nick:        m.Nick,
				ShortPubKey: m.ShortPubKey,
				Content:     m.Content,
			}
			t.input.SetText("")
			t.updateInputLabel()
			t.app.SetFocus(t.input)
			t.updateHints()
			t.updateFocusBorders()
		})
	}()
}

func (t *tui) clearPendingReply() {
	t.pendingReply = nil
	t.updateInputLabel()
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
	prevActiveIndex := t.activeViewIndex

	t.views = state.Views
	t.activeViewIndex = state.ActiveViewIndex
	t.nick = state.Nick
	t.selfShortPubKey = state.ShortPubKey

	// Clear visible message history when switching chats.
	// Old messages will be re-rendered from the relay backlog (lookback+limit).
	if prevActiveIndex != t.activeViewIndex {
		t.pendingReply = nil
		t.output.Clear()
		t.outputMessages = nil
		t.outputRowToMsg = nil
		t.output.SetCurrentItem(0)
		t.startPullingMessagesStatus()
	}

	t.updateChatList()
	t.updateDetailsView()
	t.updateInputLabel()

	// Reset and request users for the newly active view.
	t.chatUsers = nil
	t.chatUsersByPubKey = make(map[string]client.ChatUser)
	t.rebuildParticipantColorsFull()
	t.updateUserList()
	t.requestChatUsersForActiveView()
}

// handleRelaysUpdate refreshes the list of relays.
func (t *tui) handleRelaysUpdate(event client.DisplayEvent) {
	switch p := event.Payload.(type) {
	case client.RelaysPanelUpdate:
		t.relays = p.Relays
		t.relaysUpCount = p.UpCount
		t.relaysDownCount = p.DownCount
	case []client.RelayInfo:
		t.relays = p
		t.relaysUpCount = 0
		t.relaysDownCount = 0
		for _, r := range p {
			if r.Connected {
				t.relaysUpCount++
			} else {
				t.relaysDownCount++
			}
		}
	default:
		fmt.Fprintf(t.logs, "\n[%s]ERROR: Invalid RELAYS_UPDATE payload[-]", t.theme.logErrorColor)
		return
	}
	t.updateDetailsView()
}

func (t *tui) handleChatUsersUpdate(event client.DisplayEvent) {
	users, ok := event.Payload.([]client.ChatUser)
	if !ok {
		return
	}

	t.chatUsers = users
	t.chatUsersByPubKey = make(map[string]client.ChatUser, len(users))
	for _, u := range users {
		t.chatUsersByPubKey[u.PubKey] = u
	}
	// Immediately prune based on last message timestamps.
	t.pruneStaleChatUsers(time.Now())
	t.rebuildParticipantColorsFull()
	t.updateUserList()
}

func (t *tui) handleChatUserDiscovered(event client.DisplayEvent) {
	u, ok := event.Payload.(client.ChatUser)
	if !ok {
		return
	}

	// Only show users for the currently active chat scope.
	if len(t.views) == 0 || t.activeViewIndex < 0 || t.activeViewIndex >= len(t.views) {
		return
	}
	activeView := t.views[t.activeViewIndex]
	if activeView.IsGroup {
		if !slices.Contains(activeView.Children, u.Chat) {
			return
		}
	} else {
		if u.Chat != activeView.Name {
			return
		}
	}

	if t.chatUsersByPubKey == nil {
		t.chatUsersByPubKey = make(map[string]client.ChatUser)
	}

	if existing, exists := t.chatUsersByPubKey[u.PubKey]; exists {
		// Update any new nick/hash information.
		existing.Nick = u.Nick
		existing.ShortPubKey = u.ShortPubKey
		existing.Chat = u.Chat
		existing.LastMsgAt = u.LastMsgAt
		t.chatUsersByPubKey[u.PubKey] = existing
	} else {
		t.chatUsers = append(t.chatUsers, u)
		t.chatUsersByPubKey[u.PubKey] = u
		t.assignNewParticipantColor(u.PubKey)
	}

	t.updateUserList()
}

func (t *tui) requestChatUsersForActiveView() {
	if len(t.views) == 0 || t.activeViewIndex < 0 || t.activeViewIndex >= len(t.views) {
		return
	}
	activeView := t.views[t.activeViewIndex]
	if activeView.Name == "" {
		return
	}

	t.actionsChan <- client.UserAction{
		Type:    "REQUEST_CHAT_USERS",
		Payload: activeView.Name,
	}
}

func (t *tui) maybeUpsertChatUserFromEvent(event client.DisplayEvent) {
	if event.FullPubKey == "" {
		return
	}
	if len(t.views) == 0 || t.activeViewIndex < 0 || t.activeViewIndex >= len(t.views) {
		return
	}

	activeView := t.views[t.activeViewIndex]
	isRelevant := false
	if activeView.IsGroup {
		isRelevant = slices.Contains(activeView.Children, event.Chat)
	} else {
		isRelevant = event.Chat == activeView.Name
	}
	if !isRelevant {
		return
	}

	u := client.ChatUser{
		PubKey:       event.FullPubKey,
		Nick:         event.Nick,
		ShortPubKey: event.ShortPubKey,
		LastMsgAt:   event.CreatedAt,
	}

	if existing, ok := t.chatUsersByPubKey[event.FullPubKey]; ok {
		// Update timestamp on every message so pruning is correct.
		existing.Nick = u.Nick
		existing.ShortPubKey = u.ShortPubKey
		existing.LastMsgAt = u.LastMsgAt
		t.chatUsersByPubKey[event.FullPubKey] = existing

		for i := range t.chatUsers {
			if t.chatUsers[i].PubKey == event.FullPubKey {
				t.chatUsers[i] = existing
				break
			}
		}
		return
	}

	t.chatUsers = append(t.chatUsers, u)
	t.chatUsersByPubKey[event.FullPubKey] = u
	t.assignNewParticipantColor(event.FullPubKey)
	t.updateUserList()
}

// inputShouldReceiveTabForAutocomplete is true when Tab should go to the input (pick completion)
// instead of cycling focus — global capture normally eats Tab before tview sees it.
func (t *tui) inputShouldReceiveTabForAutocomplete() bool {
	if t.app.GetFocus() != t.input {
		return false
	}
	text := t.input.GetText()
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "/block ") ||
		strings.HasPrefix(trimmed, "/unblock ") ||
		strings.HasPrefix(trimmed, "/b ") ||
		strings.HasPrefix(trimmed, "/ub ") {
		parts := strings.SplitN(text, " ", 2)
		return len(parts) >= 2 && len(t.completionEntries) > 0
	}
	nick, complete := extractNickPrefix(text)
	return !complete && nick != "" && len(t.completionEntries) > 0
}

// handleNickCompletion provides completion entries to the input field.
func (t *tui) handleNickCompletion(event client.DisplayEvent) {
	entries, ok := event.Payload.([]string)
	if !ok {
		return
	}
	t.completionEntries = entries
	if len(entries) == 1 {
		text := t.input.GetText()
		nick, complete := extractNickPrefix(text)
		if !complete && nick != "" {
			lastAt := strings.LastIndex(text, "@")
			if lastAt >= 0 {
				ent := strings.TrimSpace(entries[0])
				ent = strings.TrimPrefix(ent, "@")
				cname, _, _ := strings.Cut(ent, "#")
				if strings.HasPrefix(strings.ToLower(cname), strings.ToLower(nick)) {
					t.input.SetText(text[:lastAt] + entries[0])
					t.completionEntries = nil
					return
				}
			}
		}
	}
	t.input.Autocomplete()
}

// Run starts the TUI application.
func (t *tui) Run() error {
	return t.app.Run()
}
