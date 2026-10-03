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

// Terminal size classes. The layout is derived from the available cells alone:
// there is no minimum size at which the UI stops working.
const (
	narrowWidth = 100 // below this the sidebar moves below the messages
	tinyWidth   = 60  // below this only messages and input remain
	tinyHeight  = 12

	tinyOutputMinHeight = 5 // below this even the message view is hidden
	tinyHintsMinHeight  = 8 // below this the hint line is hidden

	logsHeight        = 3 // top log pane in the split layouts
	narrowSidebarRows = 5 // chat list + details strip in narrow layouts
	bottomHeight      = 4 // input (3 rows) + hints (1 row)
)

// layoutMode names the size class of the current terminal.
type layoutMode int

const (
	modeFull layoutMode = iota
	modeNarrow
	modeTiny
)

// maximizeMode identifies a maximized view.
type maximizeMode int

const (
	maximizeNone maximizeMode = iota
	maximizeLogs
	maximizeMessages
)

// focusArea names the focused widget for hint selection.
type focusArea int

const (
	focusOther focusArea = iota
	focusInput
	focusChats
	focusMessages
	focusLogs
	focusInfo
)

// layoutPlan says which widgets a terminal size can show. It is a pure value so
// the same decision is used for rendering, hints and key handling.
type layoutPlan struct {
	mode   layoutMode
	logs   bool
	output bool
	hints  bool
}

// layoutPlanFor picks the layout for a w×h terminal. The mapping is total: any
// size, however small, produces a usable plan (input always stays visible).
func layoutPlanFor(w, h int) layoutPlan {
	switch {
	case w < tinyWidth || h < tinyHeight:
		return layoutPlan{
			mode:   modeTiny,
			output: h >= tinyOutputMinHeight,
			hints:  h >= tinyHintsMinHeight,
		}
	case w < narrowWidth:
		return layoutPlan{mode: modeNarrow, logs: true, output: true, hints: true}
	default:
		return layoutPlan{mode: modeFull, logs: true, output: true, hints: true}
	}
}

// applyLayout adapts the widget tree to the terminal size. It runs before every
// draw but rebuilds only when the size class changed or a rebuild was
// requested, so steady-state draws stay cheap.
//
// It must not touch application-level state (tview locks are held while the
// before-draw hook runs); focus is only re-pointed at a visible widget and the
// event loop re-syncs the real focus on the next key.
func (t *tui) applyLayout(screen tcell.Screen) {
	w, h := screen.Size()
	plan := layoutPlanFor(w, h)
	if t.planApplied && plan == t.plan && !t.layoutDirty {
		return
	}
	t.plan, t.planApplied, t.layoutDirty = plan, true, false
	t.rebuildLayout(plan)
}

// rebuildLayout fills the flex containers for the given size plan.
func (t *tui) rebuildLayout(plan layoutPlan) {
	t.contentGrid.Clear()
	switch plan.mode {
	case modeFull:
		t.contentGrid.SetRows(0)
		t.contentGrid.SetColumns(0, 30)
		t.contentGrid.AddItem(t.outputView, 0, 0, 1, 1, 0, 0, false)
		t.contentGrid.AddItem(t.sidebarFlex, 0, 1, 1, 1, 0, 0, false)
	case modeNarrow:
		t.contentGrid.SetRows(0, narrowSidebarRows)
		t.contentGrid.SetColumns(0)
		t.contentGrid.AddItem(t.outputView, 0, 0, 1, 1, 0, 0, false)
		t.contentGrid.AddItem(t.sidebarFlexHorizontal, 1, 0, 1, 1, 0, 0, false)
	case modeTiny:
		t.contentGrid.SetRows(0)
		t.contentGrid.SetColumns(0)
		if plan.output {
			t.contentGrid.AddItem(t.outputView, 0, 0, 1, 1, 0, 0, false)
		}
	}

	t.bottomFlex.Clear()
	t.bottomFlex.AddItem(t.input, 0, 1, true)
	if plan.hints {
		t.bottomFlex.AddItem(t.hints, 1, 0, false)
	}

	t.mainFlex.Clear()
	if plan.logs {
		t.mainFlex.AddItem(t.logsView, logsHeight, 0, false)
	}
	t.mainFlex.AddItem(t.contentGrid, 0, 1, false)
	height := bottomHeight
	if !plan.hints {
		height--
	}
	t.mainFlex.AddItem(t.bottomFlex, height, 0, true)

	t.applyTitles(plan)
}

// applyTitles shortens widget titles when there is no room for the full text.
func (t *tui) applyTitles(plan layoutPlan) {
	if plan.mode == modeFull {
		t.logsView.SetTitle(titleLogs)
		t.outputView.SetTitle(titleMessages)
		t.chatList.SetTitle(titleChats)
		t.detailsView.SetTitle(titleInfo)
		t.input.SetTitle(titleInput)
		return
	}
	t.logsView.SetTitle(titleLogsShort)
	t.outputView.SetTitle(titleMessagesShort)
	t.chatList.SetTitle(titleChatsShort)
	t.detailsView.SetTitle(titleInfoShort)
	t.input.SetTitle(titleInputShort)
}

// visible reports whether a primitive is part of the currently rendered root.
func (t *tui) visible(p tview.Primitive) bool {
	if p == nil {
		return false
	}
	switch {
	case t.logsMaximized:
		return p == t.logsView
	case t.outputMaximized:
		return p == t.outputView
	}
	switch p {
	case t.input:
		return true
	case t.outputView:
		return t.plan.output
	case t.logsView:
		return t.plan.logs
	case t.chatList, t.detailsView:
		return t.plan.mode != modeTiny
	}
	return false
}

// firstVisible returns the primitive that should receive focus when the
// current one is not on screen.
func (t *tui) firstVisible() tview.Primitive {
	switch {
	case t.logsMaximized:
		return t.logsView
	case t.outputMaximized:
		return t.outputView
	}
	return t.input
}

// canMaximize reports whether the split layout offers a maximize toggle.
func (t *tui) canMaximize() bool { return t.plan.mode != modeTiny }

// focusAreaOf maps a primitive to the hint area.
func (t *tui) focusAreaOf(p tview.Primitive) focusArea {
	switch p {
	case t.input:
		return focusInput
	case t.chatList:
		return focusChats
	case t.outputView:
		return focusMessages
	case t.logsView:
		return focusLogs
	case t.detailsView:
		return focusInfo
	}
	return focusOther
}

// setFocus moves focus, keeping the hint line and border colors in sync.
// It must not be called from a draw hook (tview holds its own lock there).
func (t *tui) setFocus(p tview.Primitive) {
	if p == nil {
		return
	}
	t.focused = p
	t.app.SetFocus(p)
	t.updateFocusBorders()
	t.updateHints()
}

// focusIfVisible moves focus to p only when it is on screen.
func (t *tui) focusIfVisible(p tview.Primitive) {
	if t.visible(p) {
		t.setFocus(p)
	}
}

// syncFocus reconciles tview's focus with ours. Cheap enough to run on every
// key event; it heals the two drift cases (a widget was hidden by a resize, or
// tview delegated focus somewhere else).
func (t *tui) syncFocus() {
	if !t.visible(t.focused) {
		t.focused = t.firstVisible()
	}
	if t.focused != nil && t.app.GetFocus() != t.focused {
		t.app.SetFocus(t.focused)
		t.updateFocusBorders()
	}
}

// cycleFocus moves focus to the next visible primitive.
func (t *tui) cycleFocus(forward bool) {
	order := []tview.Primitive{t.input, t.chatList, t.outputView, t.logsView, t.detailsView}
	visible := make([]tview.Primitive, 0, len(order))
	for _, p := range order {
		if t.visible(p) {
			visible = append(visible, p)
		}
	}
	if len(visible) == 0 {
		return
	}
	idx := 0
	for i, p := range visible {
		if p == t.focused {
			idx = i
			break
		}
	}
	if forward {
		idx = (idx + 1) % len(visible)
	} else {
		idx = (idx - 1 + len(visible)) % len(visible)
	}
	t.setFocus(visible[idx])
}

// maximizeModeOf reports which view is currently maximized.
func (t *tui) maximizeModeOf() maximizeMode {
	switch {
	case t.logsMaximized:
		return maximizeLogs
	case t.outputMaximized:
		return maximizeMessages
	}
	return maximizeNone
}

// setMaximized switches between the split layout and a maximized log or
// message view. Must not be called from a draw hook.
func (t *tui) setMaximized(mode maximizeMode) {
	var restore tview.Primitive
	switch mode {
	case maximizeLogs:
		restore = t.logsView
		t.app.SetRoot(t.maximizedLogsFlex, true)
	case maximizeMessages:
		restore = t.outputView
		t.app.SetRoot(t.maximizedOutputFlex, true)
	default:
		restore = t.maximizedView
		if restore == nil {
			restore = t.input
		}
		t.app.SetRoot(t.mainFlex, true)
		t.layoutDirty = true
	}
	t.logsMaximized = mode == maximizeLogs
	t.outputMaximized = mode == maximizeMessages
	t.maximizedView = restore
	if !t.visible(restore) {
		restore = t.firstVisible()
	}
	t.setFocus(restore)
}

// render paints the model onto the widgets. It runs only on the tview event
// loop: from the before-draw hook and after local input changes. The model lock
// is never held while widgets are touched, so widget callbacks (for example the
// chat-list selection handler) can safely re-enter it.
func (t *tui) render() {
	// Clear before taking the snapshot: later collector updates must retain
	// their wakeup even if they arrive while these widgets are being painted.
	t.dirty.Store(false)
	s := t.snapshot()
	if t.restoreTheme(s.themeName) {
		s = t.snapshot() // Theme changes invalidate both scrollback cursors.
	}
	if t.visible(t.outputView) {
		t.writeLines(t.outputView, s.output)
		t.setOutputSynced(s.output.upto, s.output.first)
	}
	if t.visible(t.logsView) {
		t.writeLines(t.logsView, s.logs)
		t.setLogsSynced(s.logs.upto, s.logs.first)
	}
	t.renderChatList(s)
	t.renderDetails(s)
	t.renderCompletion(s)
	t.renderInputLabel(s)
	t.updateFocusBorders()
	t.renderHints(s)
}

// writeLines writes the pending lines into a scrollback view. Incremental
// appends keep the work proportional to what arrived; a rewrite happens only
// whenever the model evicts lines, even while the user has scrolled to the top.
// TextView.SetMaxLines alone only evicts lines above the visible viewport.
func (t *tui) writeLines(view *tview.TextView, delta lineDelta) {
	if len(delta.lines) == 0 {
		return
	}
	if delta.rewrite {
		view.SetText(t.colorScrollback("\n" + strings.Join(delta.lines, "\n")))
		return
	}
	var b strings.Builder
	for _, line := range delta.lines {
		b.WriteString("\n")
		b.WriteString(line)
	}
	fmt.Fprint(view, t.colorScrollback(b.String()))
}

func (t *tui) colorScrollback(text string) string {
	if t.theme == defaultTheme {
		return text // Keep the default-theme hot path allocation-free.
	}
	return t.themeColors.Replace(text)
}

// renderChatList rebuilds the chat list only when client state or the local
// selection changed: rebuilding clears the widget, so it must stay rare.
func (t *tui) renderChatList(s modelSnapshot) {
	if len(s.views) == 0 && t.chatListRev == s.stateRev {
		return
	}
	if s.stateRev == t.chatListRev && t.selectionRev == t.chatListSelectionRev {
		return
	}
	t.pruneGroupSelection(s.views)

	current := t.chatList.GetCurrentItem()
	t.chatList.Clear()
	for i, view := range s.views {
		prefix := " "
		isActive := i == s.activeIndex
		isSelected := t.selectedForGroup[view.Name]
		switch {
		case isActive && isSelected && !view.IsGroup:
			prefix = "⊛"
		case isActive:
			prefix = "▶"
		case isSelected:
			prefix = "⊕"
		}
		name := sanitizeLineForDisplay(view.Name)
		if view.PoW > 0 {
			name = fmt.Sprintf("%s [PoW:%d]", name, view.PoW)
		}
		t.chatList.AddItem(fmt.Sprintf(" %s %s", prefix, name), "", 0, nil)
	}
	if current >= len(s.views) {
		current = len(s.views) - 1
	}
	if current < 0 {
		current = 0
	}
	t.chatList.SetCurrentItem(current)
	t.chatListRev = s.stateRev
	t.chatListSelectionRev = t.selectionRev
}

// pruneGroupSelection drops selections for chats that no longer exist.
func (t *tui) pruneGroupSelection(views []client.View) {
	if len(t.selectedForGroup) == 0 {
		return
	}
	known := make(map[string]bool, len(views))
	for _, v := range views {
		known[v.Name] = true
	}
	for name := range t.selectedForGroup {
		if !known[name] {
			delete(t.selectedForGroup, name)
		}
	}
}

// detailsPaneKey identifies what the details pane currently shows.
type detailsPaneKey struct {
	stateRev  int64
	relaysRev int64
	index     int
}

// updateDetailsView refreshes the info pane from the current model. It is the
// callback for chat-list selection changes.
func (t *tui) updateDetailsView() { t.renderDetails(t.snapshot()) }

// renderDetails refreshes the info pane: group members, or relay status for a
// chat. Only rebuilt when its inputs changed.
func (t *tui) renderDetails(s modelSnapshot) {
	index := t.chatList.GetCurrentItem()
	if index >= len(s.views) || index < 0 {
		index = -1
	}
	key := detailsPaneKey{stateRev: s.stateRev, relaysRev: s.relaysRev, index: index}
	if key == t.detailsKey && t.detailsKeySet {
		return
	}
	t.detailsKey = key
	t.detailsKeySet = true

	t.detailsView.Clear()
	if index < 0 {
		return
	}
	selected := s.views[index]
	if selected.IsGroup {
		var b strings.Builder
		b.WriteString(fmt.Sprintf("%sChats of %s:[-]\n", colorTag(t.theme.logWarnColor), sanitizeLineForDisplay(selected.Name)))
		for _, child := range selected.Children {
			b.WriteString(fmt.Sprintf(" - %s\n", sanitizeLineForDisplay(child)))
		}
		fmt.Fprint(t.detailsView, b.String())
		return
	}

	relays := append([]client.RelayInfo(nil), s.relays...)
	sort.SliceStable(relays, func(i, j int) bool { return relays[i].URL < relays[j].URL })
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%sConnected Relays:[-]\n", colorTag(t.theme.logWarnColor)))
	if len(relays) == 0 {
		b.WriteString(fmt.Sprintf(" %sNot connected...[-]\n", colorTag(t.theme.logInfoColor)))
	}
	for _, r := range relays {
		statusColor, symbol := t.theme.titleColor, "●"
		switch {
		case !r.Connected:
			statusColor, symbol = t.theme.logErrorColor, "×"
		case r.Latency > 750*time.Millisecond:
			statusColor, symbol = t.theme.logWarnColor, "●"
		}
		host := strings.TrimPrefix(strings.TrimPrefix(r.URL, "wss://"), "ws://")
		b.WriteString(fmt.Sprintf(" %s%s[-] %s\n", colorTag(statusColor), symbol, sanitizeLineForDisplay(host)))
	}
	fmt.Fprint(t.detailsView, b.String())
}

// renderCompletion forwards a completed nickname list to the input field.
func (t *tui) renderCompletion(s modelSnapshot) {
	if s.completionRev == t.completionApplied {
		return
	}
	t.completionApplied = s.completionRev
	t.completionEntries = s.completion
	t.input.Autocomplete()
}

// renderInputLabel keeps the prompt label in sync with the active nick.
func (t *tui) renderInputLabel(s modelSnapshot) {
	label := "> "
	if t.plan.mode != modeTiny && s.nick != "" {
		label = s.nick + " > "
	}
	if label == t.inputLabel {
		return
	}
	t.inputLabel = label
	t.input.SetLabel(label)
}

// updateFocusBorders highlights the focused widget's border.
func (t *tui) updateFocusBorders() {
	unfocused := t.theme.borderColor
	focused := t.theme.titleColor
	for _, p := range []tview.Primitive{t.logsView, t.chatList, t.detailsView, t.outputView, t.input} {
		if box, ok := p.(interface{ SetBorderColor(tcell.Color) *tview.Box }); ok {
			color := unfocused
			if p == t.focused {
				color = focused
			}
			box.SetBorderColor(color)
		}
	}
}

// updateHints refreshes the hint line from current state.
func (t *tui) updateHints() { t.renderHints(t.snapshot()) }

// renderHints writes the context-sensitive hint line.
func (t *tui) renderHints(s modelSnapshot) {
	text := hintsText(hintsState{
		highlight: colorTag(t.theme.titleColor),
		warn:      colorTag(t.theme.logWarnColor),
		mode:      t.plan.mode,
		maximized: t.maximizeModeOf(),
		area:      t.focusAreaOf(t.focused),
		notice:    backpressureNotice(s.dropped, s.dropNotice),
	})
	if text == t.hintsText {
		return
	}
	t.hintsText = text
	t.hints.SetText(text)
}

// hintsState carries the inputs of the hint line so the text is easy to test.
type hintsState struct {
	highlight string
	warn      string
	mode      layoutMode
	maximized maximizeMode
	area      focusArea
	notice    string
}

// backpressureNotice describes dropped input for the hint line.
func backpressureNotice(dropped int, active bool) string {
	if !active || dropped == 0 {
		return ""
	}
	return fmt.Sprintf("client busy: %d input(s) not sent", dropped)
}

// hintsText renders the hint line. Tiny terminals get a shorter variant that
// only advertises what is reachable at that size.
func hintsText(s hintsState) string {
	var text string
	switch {
	case s.maximized != maximizeNone:
		text = fmt.Sprintf("[%[1]s]`[-]: Restore | [%[1]s]↑/↓[-]: Scroll | [%[1]s]Ctrl+C[-]: Quit", s.highlight)
	case s.mode == modeTiny:
		text = fmt.Sprintf("[%[1]s]Tab[-]: Focus | [%[1]s]/help[-]: Commands | [%[1]s]Ctrl+C[-]: Quit", s.highlight)
	default:
		base := fmt.Sprintf("[%[1]s]Alt+...[-]: Focus | [%[1]s]Ctrl+C[-]: Quit", s.highlight)
		switch s.area {
		case focusInput:
			text = fmt.Sprintf("[%[1]s]Enter[-]: Send | [%[1]s]Ctrl+P/N[-]: History | [%[1]s]Tab/Shift+Tab[-]: Cycle Focus | %s", s.highlight, base)
		case focusMessages:
			text = fmt.Sprintf("[%[1]s]`[-]: Maximize | [%[1]s]↑/↓[-]: Scroll | [%[1]s]Tab/Shift+Tab[-]: Cycle Focus | %s", s.highlight, base)
		case focusInfo:
			text = fmt.Sprintf("[%[1]s]↑/↓[-]: Scroll | [%[1]s]Tab/Shift+Tab[-]: Cycle Focus | %s", s.highlight, base)
		case focusChats:
			text = fmt.Sprintf("[%[1]s]Space[-]: Select | [%[1]s]Enter[-]: Activate | [%[1]s]Del[-]: Delete | [%[1]s]Tab/Shift+Tab[-]: Cycle Focus | %s", s.highlight, base)
		case focusLogs:
			text = fmt.Sprintf("[%[1]s]`[-]: Maximize | [%[1]s]Tab/Shift+Tab[-]: Cycle Focus | %s", s.highlight, base)
		default:
			text = base
		}
	}
	if s.notice != "" {
		text += fmt.Sprintf(" | [%s]%s[-]", s.warn, s.notice)
	}
	return text
}
