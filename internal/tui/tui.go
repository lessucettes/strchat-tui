// Package tui renders the strchat client with tview/tcell.
//
// The package keeps three concerns apart:
//
//   - collection: one goroutine (started and joined by Run) folds client events
//     into a bounded model; it never touches a widget,
//   - rendering: runs only on the tview event loop, coalesced by a redraw tick,
//   - input: tview key handlers that submit actions without ever blocking.
//
// Run owns every goroutine and all process-global state it installs, so a
// failed or stopped UI leaves nothing behind.
package tui

import (
	"errors"
	"fmt"
	"io"
	"log"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/lessucettes/strchat-tui/internal/client"
)

const (
	// renderInterval caps how often model changes are painted: many incoming
	// events are coalesced into one redraw, so sustained traffic cannot make
	// the UI draw once per message.
	renderInterval = 40 * time.Millisecond

	// wakeKey identifies the internal repaint event posted by the render loop.
	// The global input capture consumes it; no user-visible binding uses it.
	wakeKey = tcell.KeyF64
)

// wakeRune is unused by the wake event, which is identified by its key.
const wakeRune rune = 0

// tui is the whole TUI: widgets, model and lifecycle.
type tui struct {
	app         *tview.Application
	actionsChan chan<- client.UserAction
	events      <-chan client.DisplayEvent

	// Lifecycle. Run starts and joins the collector, the redraw loop and the
	// shutdown supervisor.
	started      atomic.Bool
	quitOnce     sync.Once
	quitCh       chan struct{}
	doneCh       chan struct{}
	workers      sync.WaitGroup
	screen       atomic.Pointer[tcell.Screen]
	screenReady  chan struct{}
	screenOnce   sync.Once
	logRedirects logRedirect

	// Shared model. Written by the collector goroutine (and by the render pass
	// for the sync cursors), read by the render pass; guarded by mu.
	mu              sync.Mutex
	views           []client.View
	activeViewIndex int
	nick            string
	themeName       string
	relays          []client.RelayInfo
	stateRev        int64
	relaysRev       int64
	completion      []string
	completionRev   int64
	output          *lineBuffer
	logs            *lineBuffer
	outputSynced    int64
	logsSynced      int64
	outputFirst     int64
	logsFirst       int64
	dropped         int
	dropNotice      bool

	// dirty marks model changes that the redraw loop has not painted yet.
	dirty atomic.Bool

	// View state. Only touched on the tview event loop.
	focused              tview.Primitive
	plan                 layoutPlan
	planApplied          bool
	layoutDirty          bool
	logsMaximized        bool
	outputMaximized      bool
	maximizedView        tview.Primitive
	selectedForGroup     map[string]bool
	selectionRev         int64
	chatListRev          int64
	chatListSelectionRev int64
	detailsKey           detailsPaneKey
	detailsKeySet        bool
	completionApplied    int64
	completionEntries    []string
	inputHistory         []string
	historyOffset        int
	inputDraft           string
	lastNickQuery        string
	inputLabel           string
	hintsText            string
	theme                *theme
	themeColors          *strings.Replacer

	// Widgets.
	mainFlex              *tview.Flex
	contentGrid           *tview.Grid
	sidebarFlex           *tview.Flex
	sidebarFlexHorizontal *tview.Flex
	bottomFlex            *tview.Flex
	chatList              *tview.List
	detailsView           *tview.TextView
	logsView              *tview.TextView
	outputView            *tview.TextView
	input                 *tview.InputField
	hints                 *tview.TextView
	maximizedLogsFlex     *tview.Flex
	maximizedOutputFlex   *tview.Flex
}

// logRedirect remembers the standard-logger settings that New replaced.
type logRedirect struct {
	writer io.Writer
	flags  int
	active bool
}

// New creates the TUI: widgets, key bindings and the responsive root layout.
//
// New starts no goroutine and never blocks. It does redirect the standard
// logger into the bounded log view (restored by Run's cleanup).
func New(actions chan<- client.UserAction, events <-chan client.DisplayEvent) *tui {
	t := &tui{
		app:               tview.NewApplication(),
		actionsChan:       actions,
		events:            events,
		quitCh:            make(chan struct{}),
		doneCh:            make(chan struct{}),
		screenReady:       make(chan struct{}),
		selectedForGroup:  make(map[string]bool),
		completionEntries: []string{},
		output:            newLineBuffer(maxOutputLines, maxOutputBytes),
		logs:              newLineBuffer(maxLogLines, maxLogBytes),
		outputSynced:      -1,
		logsSynced:        -1,
		theme:             defaultTheme,
	}
	t.setupViews()
	t.setupHandlers()
	t.redirectLog()

	t.app.SetRoot(t.mainFlex, true)
	t.setFocus(t.input)
	t.render()
	return t
}

// Run starts the TUI and blocks until the UI stops. It returns nil for a normal
// exit and the terminal error when the screen could not be initialised.
//
// Run owns the TUI's goroutines: the event collector and the redraw loop are
// started here and joined here, so a failed Run leaves nothing behind.
//
// Integration contract for main:
//
//   - the events channel closing stops the UI (the listener exits and the app
//     stops), so main may close it after client.Run returns,
//   - after Run returns, call client.Stop and wait for the client to finish;
//     the UI never waits for the client,
//   - never close the actions channel while the UI runs: sending on a closed
//     channel panics, and the UI cannot detect closure of a send-only channel,
//   - Stop may be called from any goroutine (also before Run) if something
//     other than the events channel has to take the UI down.
func (t *tui) Run() error {
	if !t.started.CompareAndSwap(false, true) {
		return errors.New("tui: Run called twice")
	}
	defer t.shutdown()
	if t.isQuitRequested() {
		return nil // Stop was called before Run started: nothing to show.
	}
	t.startWorkers()
	return t.app.Run()
}

// Stop asks the UI to shut down, making Run return as soon as the event loop
// can stop. It is safe to call from any goroutine, before or after Run, and
// more than once.
func (t *tui) Stop() { t.requestQuit() }

// requestQuit records the shutdown request. The supervisor performs the actual
// tview Stop once the screen exists (tview's Stop is a no-op before that).
func (t *tui) requestQuit() {
	t.quitOnce.Do(func() { close(t.quitCh) })
}

// isQuitRequested reports whether shutdown was already requested.
func (t *tui) isQuitRequested() bool {
	select {
	case <-t.quitCh:
		return true
	default:
		return false
	}
}

// startWorkers launches the collector, the redraw loop and the supervisor. Each
// goroutine exits when doneCh closes or its own channel ends; none of them can
// block on a stopped tview application.
func (t *tui) startWorkers() {
	t.workers.Add(3)
	go t.collectEvents()
	go t.redrawLoop()
	go t.supervise()
}

// shutdown joins the workers and restores process-global state. It runs once,
// from Run, whatever the exit reason (including a failed app.Run).
func (t *tui) shutdown() {
	close(t.doneCh)
	t.workers.Wait()
	t.screen.Store(nil)
	t.restoreLog()
}

// supervise stops the application loop after a shutdown request. It waits for
// the first draw, because tview's Stop does nothing before the screen exists,
// and exits without stopping anything when Run returned for another reason.
func (t *tui) supervise() {
	defer t.workers.Done()
	select {
	case <-t.quitCh:
	case <-t.doneCh:
		return
	}
	select {
	case <-t.screenReady:
		t.app.Stop()
	case <-t.doneCh:
	}
}

// collectEvents folds client events into the model until the events channel is
// closed, a "SHUTDOWN" event arrives, or Run shuts the UI down.
func (t *tui) collectEvents() {
	defer t.workers.Done()
	for {
		select {
		case <-t.doneCh:
			return
		case ev, ok := <-t.events:
			if !ok {
				t.requestQuit()
				return
			}
			if ev.Type == "SHUTDOWN" {
				t.requestQuit()
				return
			}
			t.applyEvent(ev)
		}
	}
}

// redrawLoop coalesces model changes into at most one repaint per
// renderInterval and wakes the event loop with a non-blocking event post.
func (t *tui) redrawLoop() {
	defer t.workers.Done()
	ticker := time.NewTicker(renderInterval)
	defer ticker.Stop()
	for {
		select {
		case <-t.doneCh:
			return
		case <-ticker.C:
			if t.dirty.Load() {
				t.wake()
			}
		}
	}
}

// wake asks the event loop to draw. tcell's PostEvent never blocks: a full or
// finalized queue drops the request and the next model change retries.
func (t *tui) wake() {
	scr := t.screen.Load()
	if scr == nil {
		return
	}
	_ = (*scr).PostEvent(tcell.NewEventKey(wakeKey, wakeRune, tcell.ModNone))
}

// captureScreen remembers the screen (so the redraw loop can wake the event
// loop without ever touching tview queues, which block forever once the loop
// stopped) and releases the first-draw signal used by the supervisor.
func (t *tui) captureScreen(screen tcell.Screen) {
	t.screen.Store(&screen)
	t.screenOnce.Do(func() { close(t.screenReady) })
}

// applyEvent folds one client event into the model. It never touches widgets:
// rendering happens later, on the event loop.
func (t *tui) applyEvent(ev client.DisplayEvent) {
	switch ev.Type {
	case "NEW_MESSAGE":
		t.applyNewMessage(ev)
	case "INFO":
		t.appendOutput(formatInfo(ev.Content, defaultTheme.titleColor))
	case "STATUS", "ERROR":
		t.applyLog(ev)
	case "STATE_UPDATE":
		t.applyStateUpdate(ev)
	case "RELAYS_UPDATE":
		t.applyRelaysUpdate(ev)
	case "NICK_COMPLETION_RESULT":
		t.applyCompletion(ev)
	case "THEME_UPDATE":
		t.mu.Lock()
		t.themeName = ev.Content
		t.mu.Unlock()
		t.dirty.Store(true)
	}
}

// applyNewMessage appends a chat message when it belongs to the active view.
func (t *tui) applyNewMessage(ev client.DisplayEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if len(t.views) == 0 || t.activeViewIndex < 0 || t.activeViewIndex >= len(t.views) {
		return
	}
	active := t.views[t.activeViewIndex]
	matches := ev.Chat == active.Name
	if active.IsGroup {
		matches = slices.Contains(active.Children, ev.Chat)
	}
	if !matches {
		return
	}

	style := messageStyle{
		nickTag:   pubkeyToColor(ev.FullPubKey, defaultTheme.nickPalette),
		ownColor:  defaultTheme.inputTextColor,
		metaColor: defaultTheme.logInfoColor,
	}
	if t.nick != "" {
		style.mention = "@" + t.nick
	}
	if active.IsGroup {
		style.label = fmt.Sprintf("%s%s[-] ", colorTag(defaultTheme.titleColor), sanitizeLineForDisplay(ev.Chat))
	}
	t.output.append(formatMessage(ev, style))
	t.dirty.Store(true)
}

// applyLog appends a STATUS/ERROR line to the log scrollback.
func (t *tui) applyLog(ev client.DisplayEvent) {
	color := defaultTheme.logWarnColor
	if ev.Type == "ERROR" {
		color = defaultTheme.logErrorColor
	}
	t.appendLogLine(formatLogLine(ev.Type, ev.Content, color, time.Now().Format("15:04:05")))
}

// applyStateUpdate replaces the chat/group state.
func (t *tui) applyStateUpdate(ev client.DisplayEvent) {
	state, ok := ev.Payload.(client.StateUpdate)
	if !ok {
		t.appendLogLine(formatLogLine("ERROR", "invalid STATE_UPDATE payload", defaultTheme.logErrorColor, time.Now().Format("15:04:05")))
		return
	}
	t.mu.Lock()
	t.views = slices.Clone(state.Views)
	t.activeViewIndex = state.ActiveViewIndex
	t.nick = state.Nick
	t.stateRev++
	t.mu.Unlock()
	t.dirty.Store(true)
}

// applyRelaysUpdate replaces the relay status list.
func (t *tui) applyRelaysUpdate(ev client.DisplayEvent) {
	relays, ok := ev.Payload.([]client.RelayInfo)
	if !ok {
		t.appendLogLine(formatLogLine("ERROR", "invalid RELAYS_UPDATE payload", defaultTheme.logErrorColor, time.Now().Format("15:04:05")))
		return
	}
	t.mu.Lock()
	t.relays = slices.Clone(relays)
	t.relaysRev++
	t.mu.Unlock()
	t.dirty.Store(true)
}

// applyCompletion stores nickname completion candidates for the input field.
func (t *tui) applyCompletion(ev client.DisplayEvent) {
	entries, ok := ev.Payload.([]string)
	if !ok {
		return
	}
	t.mu.Lock()
	t.completion = slices.Clone(entries)
	t.completionRev++
	t.mu.Unlock()
	t.dirty.Store(true)
}

// appendOutput adds one rendered line to the bounded message scrollback.
func (t *tui) appendOutput(line string) {
	t.mu.Lock()
	t.output.append(line)
	t.mu.Unlock()
	t.dirty.Store(true)
}

// appendLogLine adds one rendered line to the bounded log scrollback.
func (t *tui) appendLogLine(line string) {
	t.mu.Lock()
	t.logs.append(line)
	t.mu.Unlock()
	t.dirty.Store(true)
}

// lineDelta is the set of lines a scrollback view still has to write.
type lineDelta struct {
	rewrite bool     // rebuild the view from lines instead of appending
	lines   []string // display lines in order
	upto    int64    // absolute index of the last included line
	first   int64    // oldest retained model line, independent of scroll position
}

// modelSnapshot is a consistent copy of the shared model for one render pass.
type modelSnapshot struct {
	views         []client.View
	activeIndex   int
	nick          string
	themeName     string
	relays        []client.RelayInfo
	stateRev      int64
	relaysRev     int64
	completion    []string
	completionRev int64
	dropped       int
	dropNotice    bool
	output        lineDelta
	logs          lineDelta
}

// snapshot copies the shared model under the lock. View and relay lists are
// client-bounded; scrollback arrives as deltas.
func (t *tui) snapshot() modelSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	return modelSnapshot{
		views:         slices.Clone(t.views),
		activeIndex:   t.activeViewIndex,
		nick:          t.nick,
		themeName:     t.themeName,
		relays:        slices.Clone(t.relays),
		stateRev:      t.stateRev,
		relaysRev:     t.relaysRev,
		completion:    slices.Clone(t.completion),
		completionRev: t.completionRev,
		dropped:       t.dropped,
		dropNotice:    t.dropNotice,
		output:        t.lineDeltaLocked(t.output, t.outputSynced, t.outputFirst),
		logs:          t.lineDeltaLocked(t.logs, t.logsSynced, t.logsFirst),
	}
}

// lineDeltaLocked returns the lines after the sync cursor. When the cursor fell
// out of the buffer the whole retained tail is returned for a rewrite.
func (t *tui) lineDeltaLocked(buf *lineBuffer, synced, first int64) lineDelta {
	if lines, ok := buf.unsynced(synced + 1); ok && first == buf.firstIndex() {
		return lineDelta{lines: lines, upto: buf.lastIndex(), first: first}
	}
	return lineDelta{rewrite: true, lines: buf.snapshot(), upto: buf.lastIndex(), first: buf.firstIndex()}
}

// setOutputSynced records that the message view wrote everything up to upto.
func (t *tui) setOutputSynced(upto, first int64) {
	t.mu.Lock()
	t.outputSynced = upto
	t.outputFirst = first
	t.mu.Unlock()
}

// setLogsSynced records that the log view wrote everything up to upto.
func (t *tui) setLogsSynced(upto, first int64) {
	t.mu.Lock()
	t.logsSynced = upto
	t.logsFirst = first
	t.mu.Unlock()
}

// submit hands an action to the client without ever blocking the UI thread. It
// reports false when the client's queue is full so the caller can surface the
// backpressure to the user.
func (t *tui) submit(action client.UserAction) bool {
	select {
	case t.actionsChan <- action:
		t.clearBackpressure()
		return true
	default:
		t.noteBackpressure(action.Type)
		return false
	}
}

// noteBackpressure records a dropped action. The first drop of a burst is
// logged once; the hint line carries the running state.
func (t *tui) noteBackpressure(actionType string) {
	t.mu.Lock()
	first := !t.dropNotice
	t.dropped++
	t.dropNotice = true
	t.mu.Unlock()
	if first {
		t.appendLogLine(formatLogLine("WARN",
			fmt.Sprintf("client queue full: dropped %q (client is not keeping up)", sanitizeLineForDisplay(actionType)),
			defaultTheme.logWarnColor, time.Now().Format("15:04:05")))
	}
	t.updateHints()
}

// clearBackpressure resets the drop state after a successful submission.
func (t *tui) clearBackpressure() {
	t.mu.Lock()
	dropped, hadNotice := t.dropped, t.dropNotice
	t.dropped, t.dropNotice = 0, false
	t.mu.Unlock()
	if !hadNotice {
		return
	}
	if dropped > 0 {
		t.appendLogLine(formatLogLine("STATUS",
			fmt.Sprintf("client queue recovered: %d input(s) were dropped", dropped),
			defaultTheme.logInfoColor, time.Now().Format("15:04:05")))
	}
	t.updateHints()
}

// redirectLog captures standard-library log output into the bounded log view.
func (t *tui) redirectLog() {
	t.logRedirects = logRedirect{writer: log.Writer(), flags: log.Flags(), active: true}
	log.SetFlags(0)
	log.SetOutput(logSink{t: t})
}

// restoreLog puts the process logger back the way New found it.
func (t *tui) restoreLog() {
	if !t.logRedirects.active {
		return
	}
	log.SetOutput(t.logRedirects.writer)
	log.SetFlags(t.logRedirects.flags)
	t.logRedirects.active = false
}

// logSink routes logger output into the model. Writes come from arbitrary
// goroutines, so they never touch widgets and never block.
type logSink struct{ t *tui }

func (s logSink) Write(p []byte) (int, error) {
	ts := time.Now().Format("15:04:05")
	for _, line := range splitLogLines(string(p)) {
		s.t.appendLogLine(formatAutoLogLine(ts, line, defaultTheme.logInfoColor))
	}
	return len(p), nil
}

// setupViews creates the widgets and the responsive root layout.
func (t *tui) setupViews() {
	t.initViews()
	t.initLayout()
	t.applyTheme()
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

// initViews builds every widget. Scrollback views keep a bounded buffer
// (SetMaxLines) and follow the tail until the user scrolls.
func (t *tui) initViews() {
	t.logsView = tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetMaxLines(maxLogLines)
	t.logsView.SetBorder(true).SetTitle(titleLogs).SetTitleAlign(tview.AlignLeft)
	t.logsView.ScrollToEnd()

	t.chatList = tview.NewList().ShowSecondaryText(false).SetSelectedBackgroundColor(t.theme.borderColor)
	t.chatList.SetBorder(true).SetTitle(titleChats).SetTitleAlign(tview.AlignLeft)

	t.detailsView = tview.NewTextView().SetDynamicColors(true).SetScrollable(true)
	t.detailsView.SetBorder(true).SetTitle(titleInfo).SetTitleAlign(tview.AlignLeft)

	t.outputView = tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetMaxLines(maxOutputLines)
	t.outputView.SetBorder(true).SetTitle(titleMessages).SetTitleAlign(tview.AlignLeft)
	t.outputView.ScrollToEnd()

	t.input = tview.NewInputField().
		SetLabelStyle(tcell.StyleDefault.Foreground(t.theme.titleColor)).
		SetFieldBackgroundColor(t.theme.inputBgColor).
		SetFieldTextColor(t.theme.inputTextColor)
	t.input.SetBorder(true).SetTitle(titleInput).SetTitleAlign(tview.AlignLeft)
	t.input.SetAutocompleteFunc(t.handleAutocomplete)
	t.input.SetAcceptanceFunc(func(text string, _ rune) bool {
		return graphemeLen(text) <= client.MaxMsgLen
	})

	t.hints = tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignLeft)
}

// initLayout composes the containers and installs the size-adaptive hook.
func (t *tui) initLayout() {
	t.sidebarFlex = tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(t.chatList, 0, 1, true).
		AddItem(t.detailsView, 0, 1, false)

	t.sidebarFlexHorizontal = tview.NewFlex().
		SetDirection(tview.FlexColumn).
		AddItem(t.chatList, 0, 1, true).
		AddItem(t.detailsView, 0, 1, false)

	t.contentGrid = tview.NewGrid().SetBorders(false)
	t.bottomFlex = tview.NewFlex().SetDirection(tview.FlexRow)

	t.mainFlex = tview.NewFlex().SetDirection(tview.FlexRow)

	t.maximizedLogsFlex = tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(t.logsView, 0, 1, true).
		AddItem(t.hints, 1, 0, false)

	t.maximizedOutputFlex = tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(t.outputView, 0, 1, true).
		AddItem(t.hints, 1, 0, false)

	// BeforeDraw is the only per-draw hook tview offers. It runs while tview
	// holds its own lock, so it may mutate widgets but must not call
	// application methods (SetRoot/SetFocus/QueueUpdate would deadlock).
	t.app.SetBeforeDrawFunc(func(screen tcell.Screen) bool {
		t.captureScreen(screen)
		t.applyLayout(screen)
		t.render()
		return false
	})
}
