package tui

// Functional tests drive the real TUI on a tcell simulation screen: events go
// through the events channel, keys are injected into the screen, and assertions
// read the rendered cells (what a user would actually see).
//
// tview's getters do not lock, so every read of widget or loop-owned state goes
// through the event loop (onLoopValue).

import (
	"fmt"
	"log"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/lessucettes/strchat-tui/internal/client"
)

// simTUI is a TUI running on a simulation screen.
type simTUI struct {
	t         *testing.T
	ui        *tui
	screen    tcell.SimulationScreen
	actions   chan client.UserAction
	events    chan client.DisplayEvent
	runDone   chan error
	runErr    error
	runWaited bool
}

// newSimTUI boots a TUI at the given size and waits for its first frame.
func newSimTUI(t *testing.T, w, h int) *simTUI {
	t.Helper()
	s := &simTUI{
		t:       t,
		actions: make(chan client.UserAction, 256),
		events:  make(chan client.DisplayEvent, 256),
		runDone: make(chan error, 1),
	}
	s.screen = tcell.NewSimulationScreen("UTF-8")
	s.ui = New(s.actions, s.events)
	// tview's SetScreen calls screen.Init(), which resets a simulation screen to
	// its 80x25 default, so the requested size must be applied afterwards.
	s.ui.app.SetScreen(s.screen)
	s.screen.SetSize(w, h)
	go func() { s.runDone <- s.ui.Run() }()
	select {
	case <-s.ui.screenReady:
	case <-time.After(5 * time.Second):
		t.Fatal("TUI never drew its first frame")
	}
	t.Cleanup(s.ui.restoreLog)
	t.Cleanup(s.stop)
	return s
}

// newIdleTUI creates a TUI that is never run; used for pure/unit checks.
func newIdleTUI(t *testing.T, queue int) *tui {
	t.Helper()
	ui := New(make(chan client.UserAction, queue), make(chan client.DisplayEvent))
	t.Cleanup(ui.restoreLog)
	return ui
}

// waitRun waits for Run to return, caching the result so both the test body and
// the cleanup can ask for it.
func (s *simTUI) waitRun() error {
	s.t.Helper()
	if !s.runWaited {
		s.runWaited = true
		select {
		case s.runErr = <-s.runDone:
		case <-time.After(5 * time.Second):
			s.t.Error("Run did not return")
			s.runErr = fmt.Errorf("Run did not return")
		}
	}
	return s.runErr
}

// stop shuts the UI down and requires Run to return promptly.
func (s *simTUI) stop() {
	s.ui.Stop()
	if err := s.waitRun(); err != nil {
		s.t.Errorf("Run returned error: %v", err)
	}
}

// onLoop runs f on the tview event loop. It fails the test instead of hanging
// when the loop does not run the update (the deadlock the hardening prevents).
func (s *simTUI) onLoop(f func()) {
	s.t.Helper()
	done := make(chan struct{})
	go func() {
		s.ui.app.QueueUpdate(func() {
			f()
			close(done)
		})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		s.t.Fatal("event loop did not run the queued update")
	}
}

// onLoopValue runs a read on the event loop and returns its result.
func onLoopValue[T any](s *simTUI, f func() T) T {
	s.t.Helper()
	var v T
	s.onLoop(func() { v = f() })
	return v
}

// matches reports whether f yields true on the event loop.
func (s *simTUI) matches(f func() bool) bool {
	return onLoopValue(s, f)
}

// draw flushes the model into the widgets and the screen, deterministically.
func (s *simTUI) draw() {
	s.t.Helper()
	s.onLoop(func() {
		s.ui.render()
		s.ui.app.ForceDraw()
	})
}

// resize changes the simulated terminal size and waits for the layout to adapt.
func (s *simTUI) resize(w, h int) {
	s.t.Helper()
	s.screen.SetSize(w, h)
	if err := s.screen.PostEvent(tcell.NewEventResize(w, h)); err != nil {
		s.t.Fatalf("post resize: %v", err)
	}
	waitFor(s.t, 5*time.Second, func() bool {
		return s.matches(func() bool { return s.ui.plan == layoutPlanFor(w, h) })
	})
}

// key injects one key press.
func (s *simTUI) key(key tcell.Key, r rune, mod tcell.ModMask) {
	s.t.Helper()
	s.screen.InjectKey(key, r, mod)
}

// text returns the rendered screen as a string (what the user sees).
func (s *simTUI) text() string {
	cells, w, h := s.screen.GetContents()
	var b strings.Builder
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := cells[y*w+x]
			if len(c.Runes) == 0 {
				b.WriteRune(' ')
			} else {
				b.WriteRune(c.Runes[0])
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// styleAt returns the style of a screen cell.
func (s *simTUI) styleAt(x, y int) tcell.Style {
	cells, w, _ := s.screen.GetContents()
	return cells[y*w+x].Style
}

// findByPrefix returns the coordinates of the first cell starting the given
// string, or (-1, -1).
func (s *simTUI) findByPrefix(prefix string) (x, y int) {
	cells, w, h := s.screen.GetContents()
	for yy := 0; yy < h; yy++ {
		for xx := 0; xx+len(prefix) <= w; xx++ {
			ok := true
			for i, r := range prefix {
				c := cells[yy*w+xx+i]
				if len(c.Runes) == 0 || c.Runes[0] != r {
					ok = false
					break
				}
			}
			if ok {
				return xx, yy
			}
		}
	}
	return -1, -1
}

// waitFor polls until cond holds or the deadline expires.
func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within ", d)
}

// waitGoroutines requires the goroutine count to fall back to ~want.
func waitGoroutines(t *testing.T, want, slack int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	var last int
	for time.Now().Before(deadline) {
		runtime.GC()
		last = runtime.NumGoroutine()
		if last <= want+slack {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("goroutines = %d, want <= %d", last, want+slack)
}

// waitOutput waits until the model has applied n messages.
func waitOutput(t *testing.T, s *simTUI, n int) {
	t.Helper()
	waitFor(t, 20*time.Second, func() bool {
		s.ui.mu.Lock()
		defer s.ui.mu.Unlock()
		return s.ui.output.lastIndex() >= int64(n-1)
	})
}

func testMessage(i int) client.DisplayEvent {
	return client.DisplayEvent{
		Type:        "NEW_MESSAGE",
		Chat:        "chat",
		Nick:        "alice",
		ShortPubKey: "abcd",
		FullPubKey:  "abcdef0123456789",
		Content:     fmt.Sprintf("message %d", i),
		Timestamp:   "12:00:00",
		ID:          fmt.Sprintf("id%d", i),
	}
}

func testState(views ...client.View) client.DisplayEvent {
	if len(views) == 0 {
		views = []client.View{{Name: "chat"}}
	}
	return client.DisplayEvent{
		Type: "STATE_UPDATE",
		Payload: client.StateUpdate{
			Views:           views,
			ActiveViewIndex: 0,
			Nick:            "me",
		},
	}
}

func enterText(s *simTUI, text string) {
	s.t.Helper()
	s.onLoop(func() {
		s.ui.input.SetText(text)
		s.ui.input.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), func(p tview.Primitive) {})
	})
}

func TestMessageAppearsOnScreen(t *testing.T) {
	s := newSimTUI(t, 120, 40)
	s.events <- testState()
	s.events <- testMessage(1)
	waitOutput(t, s, 1)
	s.draw()

	screen := s.text()
	if !strings.Contains(screen, "message 1") {
		t.Fatalf("message not on screen:\n%s", screen)
	}
	if !strings.Contains(screen, "alice") {
		t.Fatalf("sender not on screen:\n%s", screen)
	}
	if !strings.Contains(screen, "Messages") {
		t.Fatalf("message pane title missing:\n%s", screen)
	}
}

func TestMarkupInjectionIsNeutralized(t *testing.T) {
	s := newSimTUI(t, 120, 40)
	s.events <- testState()
	ev := testMessage(1)
	ev.Content = "hi [red]red[-] [::b]bold"
	s.events <- ev
	waitOutput(t, s, 1)
	s.draw()

	screen := s.text()
	if !strings.Contains(screen, "[red]red[-]") {
		t.Fatalf("injected markup was not rendered literally:\n%s", screen)
	}
	x, y := s.findByPrefix("[red]red[-]")
	if x < 0 {
		t.Fatalf("could not locate the injected text")
	}
	for i := 0; i < len("[red]red[-]"); i++ {
		fg, _, _ := s.styleAt(x+i, y).Decompose()
		if fg == tcell.ColorRed || fg == tcell.GetColor("#ff6347") {
			t.Fatalf("injected markup changed the color at +%d (fg=%v)", i, fg)
		}
	}
}

func TestControlCharactersAreStrippedOnScreen(t *testing.T) {
	s := newSimTUI(t, 120, 40)
	s.events <- testState()
	ev := testMessage(1)
	ev.Content = "a\x00b\x1b[31mred\u202eevil"
	s.events <- ev
	waitOutput(t, s, 1)
	s.draw()

	screen := s.text()
	if strings.ContainsRune(screen, 0x1b) || strings.ContainsRune(screen, 0x00) {
		t.Fatalf("control characters reached the screen:\n%q", screen)
	}
	if !strings.Contains(screen, "ab[31mred") {
		t.Fatalf("expected stripped content on screen:\n%s", screen)
	}
}

func TestScrollbackBudgetHoldsWhileScrolledToTop(t *testing.T) {
	ui := newIdleTUI(t, 1)
	ui.plan = layoutPlanFor(120, 40)
	ui.applyEvent(testState())
	ui.outputView.ScrollToBeginning()
	for i := range 2500 {
		ev := testMessage(i)
		ev.Content = strings.Repeat("x", 2000)
		ui.applyEvent(ev)
		if i%50 == 0 {
			ui.render()
		}
	}
	ui.render()
	if size := len(ui.outputView.GetText(false)); size > maxOutputBytes+maxOutputLines {
		t.Fatalf("scrolled message widget retained %d bytes beyond its budget", size)
	}
}

func TestRenderKeepsChangesArrivingDuringDrawDirty(t *testing.T) {
	ui := newIdleTUI(t, 1)
	ui.applyEvent(testState(client.View{Name: "first"}))
	ui.chatList.SetChangedFunc(func(int, string, string, rune) {
		ui.applyEvent(testState(client.View{Name: "second"}))
	})
	ui.render()
	if !ui.dirty.Load() {
		t.Fatal("render lost a model update that arrived after its snapshot")
	}
}

func TestLongContentIsRetainedUpToTheClientCap(t *testing.T) {
	s := newSimTUI(t, 120, 40)
	s.events <- testState()
	content := strings.Repeat("x", 4*1024)
	ev := testMessage(1)
	ev.Content = content
	s.events <- ev
	waitOutput(t, s, 1)
	stored := s.ui.modelOutputText()
	if !strings.Contains(stored, content) {
		t.Fatalf("4 KiB message was truncated (stored %d bytes)", len(stored))
	}
}

func TestSustainedLoadStaysBounded(t *testing.T) {
	s := newSimTUI(t, 120, 40)
	s.events <- testState()
	base := runtime.NumGoroutine()

	const n = 3000
	for i := 0; i < n; i++ {
		s.events <- testMessage(i)
	}
	waitOutput(t, s, n)
	s.draw()

	lines := s.ui.modelOutputCount()
	if lines != maxOutputLines {
		t.Fatalf("retained lines = %d, want %d", lines, maxOutputLines)
	}
	retained := onLoopValue(s, func() int { return s.ui.outputView.GetOriginalLineCount() })
	if retained > maxOutputLines+5 {
		t.Fatalf("text view retained %d lines, want <= %d", retained, maxOutputLines+5)
	}
	if screen := s.text(); !strings.Contains(screen, fmt.Sprintf("message %d", n-1)) {
		t.Fatalf("newest message not on screen after %d messages", n)
	}
	waitGoroutines(t, base, 4, 5*time.Second)
}

func TestActionBackpressureIsVisibleAndNonBlocking(t *testing.T) {
	s := newSimTUI(t, 120, 40)
	s.events <- testState()

	// Fill the client's queue so the next submission cannot be delivered.
	for {
		select {
		case s.actions <- client.UserAction{Type: "FILL"}:
			continue
		default:
		}
		break
	}

	enterText(s, "hello")
	s.draw()
	if got := onLoopValue(s, func() int { return s.ui.dropped }); got != 1 {
		t.Fatalf("one failed submission counted %d times", got)
	}

	if got := onLoopValue(s, func() string { return s.ui.input.GetText() }); got != "hello" {
		t.Fatalf("input text = %q, want the unsent text preserved", got)
	}
	if hints := onLoopValue(s, func() string { return s.ui.hints.GetText(true) }); !strings.Contains(hints, "client busy") {
		t.Fatalf("hint line does not show backpressure: %q", hints)
	}
	if logText := onLoopValue(s, func() string { return s.ui.logsView.GetText(true) }); !strings.Contains(logText, "queue full") {
		t.Fatalf("log does not record the drop: %q", logText)
	}

	// Draining one slot lets the next submission through and clears the notice.
	<-s.actions
	enterText(s, "again")
	if hints := onLoopValue(s, func() string { return s.ui.hints.GetText(true) }); strings.Contains(hints, "client busy") {
		t.Fatalf("backpressure notice not cleared: %q", hints)
	}
	found := false
drain:
	for {
		select {
		case a := <-s.actions:
			if a.Type == "SEND_MESSAGE" && a.Payload == "again" {
				found = true
			}
		default:
			break drain
		}
	}
	if !found {
		t.Fatal("message was not submitted after the queue drained")
	}
	if dropped := onLoopValue(s, func() int { return s.ui.dropped }); dropped != 0 {
		t.Fatalf("dropped = %d after recovery, want 0", dropped)
	}
}

func TestSubmitIsNonBlocking(t *testing.T) {
	ui := New(make(chan client.UserAction), make(chan client.DisplayEvent))
	t.Cleanup(ui.restoreLog)
	done := make(chan bool, 1)
	go func() { done <- ui.submit(client.UserAction{Type: "SEND_MESSAGE"}) }()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("submit reported success on a full channel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("submit blocked on a full channel")
	}
	if ui.dropped != 1 {
		t.Fatalf("dropped = %d, want 1", ui.dropped)
	}
}

func TestSlashCommandsArePreserved(t *testing.T) {
	cases := []struct {
		input string
		want  client.UserAction
	}{
		{"/join foo", client.UserAction{Type: "JOIN_CHATS", Payload: "foo"}},
		{"/j foo", client.UserAction{Type: "JOIN_CHATS", Payload: "foo"}},
		{"/pow 12", client.UserAction{Type: "SET_POW", Payload: "12"}},
		{"/pow", client.UserAction{Type: "SET_POW", Payload: "0"}},
		{"/list", client.UserAction{Type: "LIST_CHATS"}},
		{"/l", client.UserAction{Type: "LIST_CHATS"}},
		{"/set", client.UserAction{Type: "GET_ACTIVE_CHAT"}},
		{"/set chat", client.UserAction{Type: "ACTIVATE_VIEW", Payload: "chat"}},
		{"/set a b", client.UserAction{Type: "CREATE_GROUP", Payload: "a,b"}},
		{"/nick bob", client.UserAction{Type: "SET_NICK", Payload: "bob"}},
		{"/del x", client.UserAction{Type: "DELETE_VIEW", Payload: "x"}},
		{"/block", client.UserAction{Type: "LIST_BLOCKED"}},
		{"/block bob", client.UserAction{Type: "BLOCK_USER", Payload: "bob"}},
		{"/b bob", client.UserAction{Type: "BLOCK_USER", Payload: "bob"}},
		{"/unblock", client.UserAction{Type: "LIST_BLOCKED"}},
		{"/unblock bob", client.UserAction{Type: "UNBLOCK_USER", Payload: "bob"}},
		{"/ub bob", client.UserAction{Type: "UNBLOCK_USER", Payload: "bob"}},
		{"/filter foo", client.UserAction{Type: "HANDLE_FILTER", Payload: "foo"}},
		{"/unfilter", client.UserAction{Type: "CLEAR_FILTERS"}},
		{"/unfilter 2", client.UserAction{Type: "REMOVE_FILTER", Payload: "2"}},
		{"/mute bar", client.UserAction{Type: "HANDLE_MUTE", Payload: "bar"}},
		{"/unmute", client.UserAction{Type: "CLEAR_MUTES"}},
		{"/unmute 1", client.UserAction{Type: "REMOVE_MUTE", Payload: "1"}},
		{"/relay", client.UserAction{Type: "MANAGE_ANCHORS"}},
		{"/relay wss://example.com", client.UserAction{Type: "MANAGE_ANCHORS", Payload: "wss://example.com"}},
		{"/help", client.UserAction{Type: "GET_HELP"}},
		{"/h", client.UserAction{Type: "GET_HELP"}},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			s := newSimTUI(t, 120, 40)
			enterText(s, tc.input)
			select {
			case got := <-s.actions:
				if got != tc.want {
					t.Fatalf("%q produced %+v, want %+v", tc.input, got, tc.want)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("%q produced no action", tc.input)
			}
			if got := onLoopValue(s, func() string { return s.ui.input.GetText() }); got != "" {
				t.Fatalf("input not cleared after %q: %q", tc.input, got)
			}
		})
	}
}

func TestQuitCommandStopsUI(t *testing.T) {
	s := newSimTUI(t, 120, 40)
	enterText(s, "/quit")
	select {
	case got := <-s.actions:
		if got.Type != "QUIT" {
			t.Fatalf("action = %+v, want QUIT", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("/quit did not submit QUIT")
	}
	if err := s.waitRun(); err != nil {
		t.Fatalf("Run returned %v after /quit, want nil", err)
	}
}

func TestGroupSelectionAndCreation(t *testing.T) {
	s := newSimTUI(t, 120, 40)
	s.events <- testState(client.View{Name: "alpha"}, client.View{Name: "beta"}, client.View{Name: "gamma"})
	waitFor(t, 5*time.Second, func() bool {
		return s.matches(func() bool { return s.ui.chatList.GetItemCount() == 3 })
	})
	s.draw()

	s.onLoop(func() { s.ui.setFocus(s.ui.chatList) })
	s.key(tcell.KeyRune, ' ', tcell.ModNone)
	waitFor(t, 5*time.Second, func() bool {
		return s.matches(func() bool { return len(s.ui.selectedForGroup) == 1 })
	})
	s.key(tcell.KeyDown, 0, tcell.ModNone)
	s.key(tcell.KeyRune, ' ', tcell.ModNone)
	waitFor(t, 5*time.Second, func() bool {
		return s.matches(func() bool { return len(s.ui.selectedForGroup) == 2 })
	})
	s.key(tcell.KeyEnter, 0, tcell.ModNone)

	select {
	case got := <-s.actions:
		if got.Type != "CREATE_GROUP" || got.Payload != "alpha,beta" {
			t.Fatalf("action = %+v, want CREATE_GROUP alpha,beta", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no CREATE_GROUP action")
	}
	waitFor(t, 2*time.Second, func() bool {
		return s.matches(func() bool { return len(s.ui.selectedForGroup) == 0 })
	})
}

func TestDeleteKeyLeavesOrDeletesChat(t *testing.T) {
	s := newSimTUI(t, 120, 40)
	s.events <- testState(client.View{Name: "alpha"}, client.View{Name: "grp", IsGroup: true})
	waitFor(t, 5*time.Second, func() bool {
		return s.matches(func() bool { return s.ui.chatList.GetItemCount() == 2 })
	})
	s.onLoop(func() { s.ui.setFocus(s.ui.chatList) })
	s.key(tcell.KeyDelete, 0, tcell.ModNone)
	select {
	case got := <-s.actions:
		if got.Type != "LEAVE_CHAT" || got.Payload != "alpha" {
			t.Fatalf("action = %+v, want LEAVE_CHAT alpha", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no LEAVE_CHAT action")
	}
	s.key(tcell.KeyDown, 0, tcell.ModNone)
	s.key(tcell.KeyDelete, 0, tcell.ModNone)
	select {
	case got := <-s.actions:
		if got.Type != "DELETE_GROUP" || got.Payload != "grp" {
			t.Fatalf("action = %+v, want DELETE_GROUP grp", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no DELETE_GROUP action")
	}
}

func TestLogOutputIsCapturedAndBounded(t *testing.T) {
	s := newSimTUI(t, 120, 40)
	log.Print("hello from the logger")
	waitFor(t, 5*time.Second, func() bool {
		return s.matches(func() bool { return strings.Contains(s.ui.logsView.GetText(true), "hello from the logger") })
	})
	for i := 0; i < maxLogLines*3; i++ {
		log.Printf("line %d", i)
	}
	waitFor(t, 20*time.Second, func() bool {
		s.ui.mu.Lock()
		defer s.ui.mu.Unlock()
		return s.ui.logs.lastIndex() >= int64(maxLogLines*3)
	})
	s.draw()
	if retained := onLoopValue(s, func() int { return s.ui.logsView.GetOriginalLineCount() }); retained > maxLogLines+5 {
		t.Fatalf("log view retained %d lines, want <= %d", retained, maxLogLines+5)
	}
	if lines := s.ui.modelLogCount(); lines != maxLogLines {
		t.Fatalf("log ring retained %d lines, want %d", lines, maxLogLines)
	}
}

func TestNickCompletionResultReachesInput(t *testing.T) {
	s := newSimTUI(t, 120, 40)
	s.onLoop(func() { s.ui.input.SetText("hey @alice") })
	s.events <- client.DisplayEvent{Type: "NICK_COMPLETION_RESULT", Payload: []string{"@alice#1234"}}
	waitFor(t, 5*time.Second, func() bool {
		return s.matches(func() bool { return len(s.ui.completionEntries) == 1 })
	})
}

func TestAutocompleteKeepsBlockCompletionBehavior(t *testing.T) {
	ui := newIdleTUI(t, 1)
	ui.completionEntries = []string{"bob", "carol"}
	got := ui.handleAutocomplete("/block b")
	want := []string{"/block bob", "/block carol"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("autocomplete(/block) = %v, want %v", got, want)
	}
	if got := ui.handleAutocomplete("/block "); got != nil {
		t.Fatalf("autocomplete(/block ) = %v, want nil", got)
	}
	ui.completionEntries = []string{"@dave#1111"}
	if got := ui.handleAutocomplete("hi @da"); len(got) != 1 || got[0] != "@dave#1111" {
		t.Fatalf("autocomplete(nick) = %v", got)
	}
	if got := ui.handleAutocomplete("hi @dave#1111"); got != nil {
		t.Fatalf("autocomplete(complete nick) = %v, want nil", got)
	}
}

func TestErrorsSurfaceInLogView(t *testing.T) {
	s := newSimTUI(t, 120, 40)
	s.events <- client.DisplayEvent{Type: "ERROR", Content: "boom [x]"}
	waitFor(t, 5*time.Second, func() bool {
		return s.matches(func() bool { return strings.Contains(s.ui.logsView.GetText(true), "boom [x]") })
	})
}

// --- lifecycle -------------------------------------------------------------

func TestRunReturnsOnShutdownEvent(t *testing.T) {
	base := runtime.NumGoroutine()
	s := newSimTUI(t, 80, 24)
	s.events <- client.DisplayEvent{Type: "SHUTDOWN"}
	if err := s.waitRun(); err != nil {
		t.Fatalf("Run returned %v after SHUTDOWN, want nil", err)
	}
	waitGoroutines(t, base, 3, 5*time.Second)
}

func TestRunReturnsOnClosedEventsChannel(t *testing.T) {
	base := runtime.NumGoroutine()
	s := newSimTUI(t, 80, 24)
	close(s.events)
	if err := s.waitRun(); err != nil {
		t.Fatalf("Run returned %v after closing events, want nil", err)
	}
	waitGoroutines(t, base, 3, 5*time.Second)
}

func TestStopStopsRunningUI(t *testing.T) {
	base := runtime.NumGoroutine()
	s := newSimTUI(t, 80, 24)
	s.ui.Stop()
	if err := s.waitRun(); err != nil {
		t.Fatalf("Run returned %v after Stop, want nil", err)
	}
	waitGoroutines(t, base, 3, 5*time.Second)
}

func TestStopBeforeRunReturnsImmediately(t *testing.T) {
	base := runtime.NumGoroutine()
	ui := newIdleTUI(t, 1)
	ui.Stop()
	done := make(chan error, 1)
	go func() { done <- ui.Run() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after a pre-Run Stop")
	}
	waitGoroutines(t, base, 2, 3*time.Second)
}

func TestRunCalledTwiceReturnsError(t *testing.T) {
	s := newSimTUI(t, 80, 24)
	if err := s.ui.Run(); err == nil {
		t.Fatal("second Run returned nil, want an error")
	}
}

func TestRunFailureCleansUpCollectorAndLogger(t *testing.T) {
	base := runtime.NumGoroutine()
	prevWriter, prevFlags := log.Writer(), log.Flags()

	t.Setenv("TERM", "tui-test-no-such-terminal")
	ui := New(make(chan client.UserAction, 1), make(chan client.DisplayEvent))
	err := ui.Run()
	if err == nil {
		ui.Stop()
		t.Fatal("Run succeeded without a usable terminal, want an error")
	}
	if log.Writer() != prevWriter || log.Flags() != prevFlags {
		t.Fatal("standard logger was not restored after a failed Run")
	}
	waitGoroutines(t, base, 2, 3*time.Second)
	select {
	case <-ui.doneCh:
	default:
		t.Fatal("doneCh not closed after a failed Run")
	}
}

func TestModelChangesDoNotTouchWidgetsOffLoop(t *testing.T) {
	base := runtime.NumGoroutine()
	ui := newIdleTUI(t, 1)
	ui.applyEvent(testState())
	ui.applyEvent(testMessage(1))
	ui.applyEvent(client.DisplayEvent{Type: "INFO", Content: "info [x]"})
	ui.applyEvent(client.DisplayEvent{Type: "STATUS", Content: "status"})
	ui.applyEvent(client.DisplayEvent{Type: "RELAYS_UPDATE", Payload: []client.RelayInfo{{URL: "wss://a", Connected: true}}})
	if ui.output.lastIndex() < 0 {
		t.Fatalf("message not applied to the model")
	}
	if lines := ui.output.snapshot(); len(lines) < 2 || !strings.Contains(lines[0], "message 1") {
		t.Fatalf("output does not start with the message: %v", lines)
	}
	if ui.logs.lastIndex() != 0 {
		t.Fatalf("status not applied to the log model")
	}
	runtime.GC()
	if n := runtime.NumGoroutine(); n > base+2 {
		t.Fatalf("applying events spawned goroutines: %d > %d", n, base+2)
	}
}

// modelOutputCount and modelLogCount report retained scrollback lines under the
// model lock, so tests and benchmarks can observe the model without racing the
// collector.
func (t *tui) modelOutputCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.output.count
}

func (t *tui) modelLogCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.logs.count
}

// modelOutputText returns the retained scrollback as text.
func (t *tui) modelOutputText() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.output.snapshot(), "\n")
}
