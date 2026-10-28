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
	"maps"
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
	app         *screenHost
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
	chatNicks       map[string]string
	themeName       string
	artSelection    string
	artRevision     uint64
	scrollbarMode   client.ScrollbarPosition
	relays          []client.RelayInfo
	stateRev        int64
	relaysRev       int64
	completion      []string
	completionRev   int64
	messageTarget   client.MessageTarget
	recipientRev    uint64
	groupFocus      map[string]string // latest local choices, including pending saves
	output          *lineBuffer
	logs            *lineBuffer
	outputSynced    int64
	logsSynced      int64
	dropped         int
	dropNotice      bool
	clearPending    int // accepted clears not yet acknowledged by the client

	// dirty marks model changes that the redraw loop has not painted yet.
	// redraw starts the coalescing timer on demand, rather than polling at idle.
	dirty      atomic.Bool
	redraw     chan struct{}
	artRunning atomic.Bool
	artWake    chan struct{}

	// View state. Only touched on the tview event loop.
	focused              tview.Primitive
	plan                 layoutPlan
	planApplied          bool
	layoutDirty          bool
	infoTemporary        bool
	infoReveal           bool
	logsMaximized        bool
	outputMaximized      bool
	maximizedView        tview.Primitive
	selectedForGroup     map[string]bool
	targetQuery          client.MessageTarget
	targetQueryRev       uint64
	targetRequested      bool
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
	lastBell             time.Time
	theme                *theme
	themeColors          *strings.Replacer
	scrollbarPosition    client.ScrollbarPosition
	scrollbars           [4]*scrollbarPane
	scrollbarDrag        *scrollbarPane
	scrollbarGrab        int
	scrollbarOrigin      int64
	scrollbarPress       bool
	mouseScreen          tcell.Screen
	mouseFlags           tcell.MouseFlags

	// Widgets.
	mainFlex              *tview.Flex
	contentGrid           *tview.Grid
	sidebarFlex           *tview.Flex
	sidebarFlexHorizontal *tview.Flex
	bottomFlex            *tview.Flex
	chatList              *tview.List
	detailsView           *tview.TextView
	logsView              *scrollbackView
	outputView            *scrollbackView
	outputPane            *scrollbackView
	input                 *inputField
	hints                 *tview.TextView
	art                   *artPane
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
		app:               newScreenHost(),
		actionsChan:       actions,
		events:            events,
		quitCh:            make(chan struct{}),
		doneCh:            make(chan struct{}),
		screenReady:       make(chan struct{}),
		redraw:            make(chan struct{}, 1),
		artWake:           make(chan struct{}, 1),
		selectedForGroup:  make(map[string]bool),
		groupFocus:        make(map[string]string),
		completionEntries: []string{},
		output:            newLineBuffer(maxOutputLines, maxOutputBytes),
		logs:              newLineBuffer(maxLogLines, maxLogBytes),
		outputSynced:      -1,
		logsSynced:        -1,
		theme:             defaultTheme,
		scrollbarMode:     client.ScrollbarRight,
	}
	t.setupViews()
	t.setupHandlers()
	t.setupMouse()
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
	if err := t.installScreen(); err != nil {
		return err
	}
	t.startWorkers()
	return t.app.Run()
}

// installScreen gives tview a screen that writes only the cells a frame actually
// changes. tview clears and redraws every primitive each frame, and tcell cannot
// recognise a frame that ends where it started, so without this every keystroke
// repaints the whole screen.
//
// A screen installed by the caller (tests, embedders) is left alone: replacing
// it would finalise a screen they still hold. tview's SetScreen discards the
// error from Init, so the failure is reported from the wrapped screen instead.
func (t *tui) installScreen() error {
	if t.app.external.Load() {
		return nil
	}
	screen, err := newFrameScreen()
	if err != nil {
		return err
	}
	t.app.SetScreen(screen)
	return screen.InitError()
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
	timer := time.NewTimer(renderInterval)
	timer.Stop()
	defer timer.Stop()
	artTimer := time.NewTimer(artInterval)
	artTimer.Stop()
	defer artTimer.Stop()
	var artTick <-chan time.Time
	var tick <-chan time.Time
	var next time.Time
	for {
		select {
		case <-t.doneCh:
			return
		case <-t.artWake:
			artTimer.Stop()
			artTick = nil
			if t.artRunning.Load() {
				artTimer.Reset(artInterval)
				artTick = artTimer.C
			}
		case <-artTick:
			artTick = nil
			if t.artRunning.Load() {
				t.wake()
				artTimer.Reset(artInterval)
				artTick = artTimer.C
			}
		case <-t.redraw:
			if tick == nil && t.dirty.Load() {
				timer.Reset(max(0, time.Until(next)))
				tick = timer.C
			}
		case <-tick:
			tick = nil
			if t.dirty.Load() {
				t.wake()
				next = time.Now().Add(renderInterval)
				// Retry until painted: a full screen queue may drop the wake.
				timer.Reset(renderInterval)
				tick = timer.C
			}
		}
	}
}

// markDirty never blocks the collector or input handler, including at shutdown.
func (t *tui) markDirty() {
	if t.dirty.Swap(true) {
		return
	}
	select {
	case t.redraw <- struct{}{}:
	default:
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
	t.updateMouseReporting(screen)
	t.screenOnce.Do(func() {
		close(t.screenReady)
	})
}

// applyEvent folds one client event into the model. It never touches widgets:
// rendering happens later, on the event loop.
func (t *tui) applyEvent(ev client.DisplayEvent) {
	switch ev.Type {
	case "CLEARED":
		t.mu.Lock()
		if t.clearPending > 0 {
			t.clearPending--
		}
		t.mu.Unlock()
	case "NEW_MESSAGE":
		t.applyNewMessage(ev)
	case "INFO", "STATUS", "WARN", "ERROR":
		t.applyTextEvent(ev, false)
	case "STATE_UPDATE":
		t.applyStateUpdate(ev)
	case "RELAYS_UPDATE":
		t.applyRelaysUpdate(ev)
	case "NICK_COMPLETION_RESULT":
		t.applyCompletion(ev)
	case "MESSAGE_TARGET":
		if result, ok := ev.Payload.(client.MessageTarget); ok {
			t.mu.Lock()
			if t.clearPending == 0 {
				t.messageTarget = result
			}
			t.mu.Unlock()
			t.markDirty()
		}
	case "THEME_UPDATE":
		t.mu.Lock()
		t.themeName = ev.Content
		t.mu.Unlock()
		t.markDirty()
	case "ART_UPDATE":
		t.mu.Lock()
		t.artSelection = ev.Content
		t.artRevision++
		t.mu.Unlock()
		t.markDirty()
	case "SCROLLBAR_UPDATE":
		if position, ok := ev.Payload.(client.ScrollbarPosition); ok {
			t.mu.Lock()
			t.scrollbarMode = position
			t.mu.Unlock()
			t.markDirty()
		}
	}
}

// applyNewMessage appends a chat message when it belongs to the active view.
func (t *tui) applyNewMessage(ev client.DisplayEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.clearPending > 0 {
		return
	}
	if !ev.RecipientUnchanged {
		t.recipientRev++
	}

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
	if nick := t.chatNicks[ev.Chat]; nick != "" {
		style.mention = "@" + nick
	}
	if active.IsGroup {
		style.label = fmt.Sprintf("%s%s[-] ", colorTag(defaultTheme.titleColor), sanitizeLineForDisplay(ev.Chat))
	}
	if active.IsGroup {
		t.output.appendChat(formatMessage(ev, style), ev.Chat)
	} else {
		t.output.append(formatMessage(ev, style))
	}
	t.markDirty()
}

// applyLocalEvent keeps new UI diagnostics and command output visible even while
// buffered client text is being discarded at a clear boundary.
func (t *tui) applyLocalEvent(ev client.DisplayEvent) {
	t.applyTextEvent(ev, true)
}

func (t *tui) applyTextEvent(ev client.DisplayEvent, local bool) {
	// Only Messages output participates in clear. Operational Logs always flow.
	preserve := local || ev.Type != "INFO"
	if !preserve {
		t.mu.Lock()
		clearing := t.clearPending > 0
		t.mu.Unlock()
		if clearing {
			return // Do not format large payloads that will be discarded.
		}
	}
	var line string
	if ev.Type == "INFO" {
		line = formatInfo(ev.Content, defaultTheme.titleColor)
	} else {
		level, color := "INFO", defaultTheme.logInfoColor
		switch ev.Type {
		case "WARN":
			level, color = "WARN", defaultTheme.logWarnColor
		case "ERROR":
			level, color = "ERROR", defaultTheme.logErrorColor
		}
		line = formatLogLine(level, ev.Content, color, time.Now().Format("15:04:05"))
	}
	t.mu.Lock()
	// Clear may have started while formatting outside the model lock.
	if preserve || t.clearPending == 0 {
		if ev.Type == "INFO" {
			t.output.append(line)
		} else {
			t.logs.append(line)
		}
	}
	t.mu.Unlock()
	t.markDirty()
}

// applyStateUpdate replaces the chat/group state.
func (t *tui) applyStateUpdate(ev client.DisplayEvent) {
	state, ok := ev.Payload.(client.StateUpdate)
	if !ok {
		t.appendLogLine(formatLogLine("ERROR", "Cannot refresh chats. The client supplied an invalid state update.", defaultTheme.logErrorColor, time.Now().Format("15:04:05")))
		return
	}
	t.mu.Lock()
	t.views = slices.Clone(state.Views)
	t.activeViewIndex = state.ActiveViewIndex
	t.chatNicks = maps.Clone(state.ChatNicks)
	t.stateRev++
	// Recipient routes depend on group membership as well as incoming users.
	t.recipientRev++
	t.messageTarget = client.MessageTarget{}
	// Reconcile every snapshot, even if a group is deleted and recreated
	// between renders. The collector and keyboard handler share this model.
	t.pruneConversationFocusLocked(t.views)
	t.mu.Unlock()
	t.markDirty()
}

// applyRelaysUpdate replaces the relay status list.
func (t *tui) applyRelaysUpdate(ev client.DisplayEvent) {
	relays, ok := ev.Payload.([]client.RelayInfo)
	if !ok {
		t.appendLogLine(formatLogLine("ERROR", "Cannot refresh relay details. The client supplied an invalid relay update.", defaultTheme.logErrorColor, time.Now().Format("15:04:05")))
		return
	}
	t.mu.Lock()
	t.relays = slices.Clone(relays)
	t.relaysRev++
	t.mu.Unlock()
	t.markDirty()
}

// applyCompletion stores nickname completion candidates for the input field.
func (t *tui) applyCompletion(ev client.DisplayEvent) {
	entries, ok := ev.Payload.([]string)
	if !ok {
		return
	}
	t.mu.Lock()
	if t.clearPending == 0 {
		t.completion = slices.Clone(entries)
		t.completionRev++
	}
	t.mu.Unlock()
	t.markDirty()
}

// appendOutput adds one rendered line to the bounded message scrollback.
func (t *tui) appendOutput(line string) {
	t.mu.Lock()
	if t.clearPending == 0 {
		t.output.append(line)
	}
	t.mu.Unlock()
	t.markDirty()
}

// appendLogLine adds one rendered line to the bounded log scrollback.
func (t *tui) appendLogLine(line string) {
	t.mu.Lock()
	t.logs.append(line)
	t.mu.Unlock()
	t.markDirty()
}

// lineDelta is the contiguous, still-unwritten slice of a bounded line buffer.
// The view reconciles it by absolute index, so eviction needs a head drop, not
// a full-text rewrite.
type lineDelta struct {
	lines []string // canonical display lines, starting at from
	chats []string // structured group source for each line
	from  int64    // absolute index of lines[0]
	upto  int64    // absolute index of the last included line
	first int64    // oldest retained model line, independent of scroll position
}

// modelSnapshot is a consistent copy of the shared model for one render pass.
type modelSnapshot struct {
	views         []client.View
	activeIndex   int
	chatNicks     map[string]string
	themeName     string
	artSelection  string
	artRevision   uint64
	scrollbarMode client.ScrollbarPosition
	relays        []client.RelayInfo
	stateRev      int64
	relaysRev     int64
	completion    []string
	completionRev int64
	messageTarget client.MessageTarget
	recipientRev  uint64
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
	s := modelSnapshot{
		views:         slices.Clone(t.views),
		activeIndex:   t.activeViewIndex,
		chatNicks:     t.chatNicks, // immutable after the collector clones it
		themeName:     t.themeName,
		artSelection:  t.artSelection,
		artRevision:   t.artRevision,
		scrollbarMode: t.scrollbarMode,
		relays:        slices.Clone(t.relays),
		stateRev:      t.stateRev,
		relaysRev:     t.relaysRev,
		completion:    slices.Clone(t.completion),
		completionRev: t.completionRev,
		messageTarget: t.messageTarget,
		recipientRev:  t.recipientRev,
		dropped:       t.dropped,
		dropNotice:    t.dropNotice,
		output:        t.lineDeltaLocked(t.output, t.outputSynced),
		logs:          t.lineDeltaLocked(t.logs, t.logsSynced),
	}
	// Older acknowledgements must not roll back a newer keyboard choice.
	// Fold the local overlay into this immutable snapshot under the same lock.
	for i := range s.views {
		if chat, ok := t.groupFocus[s.views[i].Name]; s.views[i].IsGroup && ok {
			s.views[i].SelectedChat = chat
		}
	}
	return s
}

// lineDeltaLocked returns the retained lines after the sync cursor. The result
// always starts at max(synced+1, first): an append is one line, and a cursor
// that fell behind eviction gets the retained tail once.
func (t *tui) lineDeltaLocked(buf *lineBuffer, synced int64) lineDelta {
	first := buf.firstIndex()
	from := max(synced+1, first)
	last := buf.lastIndex()
	if last < first {
		return lineDelta{from: from, upto: last, first: first}
	}
	if from > last {
		return lineDelta{from: last + 1, upto: last, first: first}
	}
	return lineDelta{lines: buf.linesFrom(from), chats: buf.chatSources(from), from: from, upto: last, first: first}
}

// setOutputSynced records that the message view folded everything up to upto.
func (t *tui) setOutputSynced(upto int64) {
	t.mu.Lock()
	t.outputSynced = upto
	t.mu.Unlock()
}

// setLogsSynced records that the log view folded everything up to upto.
func (t *tui) setLogsSynced(upto int64) {
	t.mu.Lock()
	t.logsSynced = upto
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
		t.noteBackpressure()
		if action.Type != "REQUEST_NICK_COMPLETION" && action.Type != "QUIT" {
			t.bell()
		}
		return false
	}
}

// noteBackpressure records a dropped action. The first drop of a burst is
// logged once; the hint line carries the running state.
func (t *tui) noteBackpressure() {
	t.mu.Lock()
	first := !t.dropNotice
	t.dropped++
	t.dropNotice = true
	t.mu.Unlock()
	if first {
		t.applyLocalEvent(client.DisplayEvent{Type: "WARN", Content: "Client input queue is full. Some requests were not accepted; retry if needed."})
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
		t.applyLocalEvent(client.DisplayEvent{Type: "STATUS", Content: "Client input queue recovered. Retry any action that was not accepted."})
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

// initViews builds every widget. Scan scrollback is bounded by the model and
// windowed into the tview widgets by scrollbackView.
func (t *tui) initViews() {
	t.art = newArtPane(t.theme)
	t.logsView = newScrollbackView(false)
	t.logsView.SetBorder(true).SetTitle(titleLogs).SetTitleAlign(tview.AlignLeft)
	t.logsView.ScrollToEnd()

	t.chatList = tview.NewList().ShowSecondaryText(false).SetSelectedBackgroundColor(t.theme.borderColor)
	t.chatList.SetBorder(true).SetTitle(titleChats).SetTitleAlign(tview.AlignLeft)

	t.detailsView = tview.NewTextView().SetDynamicColors(true).SetScrollable(true)
	t.detailsView.SetBorder(true).SetTitle(titleInfo).SetTitleAlign(tview.AlignLeft)
	t.installInfoScroll()

	t.outputView = newScrollbackView(true)
	t.outputView.SetBorder(true).SetTitle(titleMessages).SetTitleAlign(tview.AlignLeft)
	t.outputView.ScrollToEnd()
	// The model bounds retained text. A second wrapped-line cap would discard
	// different messages on narrow screens and desynchronize the thread gutter.
	t.outputPane = t.outputView

	t.input = &inputField{InputField: tview.NewInputField().
		SetLabelStyle(tcell.StyleDefault.Foreground(t.theme.titleColor)).
		SetFieldBackgroundColor(t.theme.inputBgColor).
		SetFieldTextColor(t.theme.inputTextColor)}
	t.input.SetBorder(true).SetTitle(titleInput).SetTitleAlign(tview.AlignLeft)

	t.hints = tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignLeft)
	t.initScrollbars()
}

// initLayout composes the containers and installs the size-adaptive hook.
func (t *tui) initLayout() {
	t.sidebarFlex = tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(t.withScrollbar(t.chatList), 0, 1, true).
		AddItem(t.withScrollbar(t.detailsView), 0, 1, false)

	t.sidebarFlexHorizontal = tview.NewFlex().
		SetDirection(tview.FlexColumn).
		AddItem(t.withScrollbar(t.chatList), 0, 1, true).
		AddItem(t.withScrollbar(t.detailsView), 0, 1, false)

	t.contentGrid = tview.NewGrid().SetBorders(false)
	t.bottomFlex = tview.NewFlex().SetDirection(tview.FlexRow)

	t.mainFlex = tview.NewFlex().SetDirection(tview.FlexRow)

	t.maximizedLogsFlex = tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(t.withScrollbar(t.logsView), 0, 1, true).
		AddItem(t.hints, 1, 0, false)

	t.maximizedOutputFlex = tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(t.withScrollbar(t.outputPane), 0, 1, true).
		AddItem(t.hints, 1, 0, false)

	// BeforeDraw is the only per-draw hook tview offers. It runs while tview
	// holds its own lock, so it may mutate widgets but must not call
	// application methods (SetRoot/SetFocus/QueueUpdate would deadlock).
	t.app.SetBeforeDrawFunc(func(screen tcell.Screen) bool {
		t.captureScreen(screen)
		t.applyLayout(screen)
		t.render()
		t.syncArt()
		return false
	})
}
