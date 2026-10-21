package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/lessucettes/strchat-tui/internal/client"
)

type countingBellScreen struct {
	tcell.Screen
	bells int
}

func (s *countingBellScreen) Beep() error { s.bells++; return nil }

func TestCtrlDQuitsFromEveryPane(t *testing.T) {
	for _, pane := range []string{"input", "chats", "messages", "logs", "info", "maximized messages", "maximized logs", "tiny"} {
		t.Run(pane, func(t *testing.T) {
			s := newSimTUI(t, 140, 40)
			if pane == "tiny" {
				s.resize(40, 8)
			}
			s.onLoop(func() {
				focus := map[string]tview.Primitive{"input": s.ui.input, "chats": s.ui.chatList, "messages": s.ui.outputView, "logs": s.ui.logsView, "info": s.ui.detailsView}[pane]
				if focus != nil {
					s.ui.setFocus(focus)
				}
				if pane == "maximized messages" {
					s.ui.setMaximized(maximizeMessages)
				}
				if pane == "maximized logs" {
					s.ui.setMaximized(maximizeLogs)
				}
			})
			s.key(tcell.KeyCtrlC, 0, tcell.ModNone)
			// The input event following Ctrl+C must still reach the live loop.
			s.key(tcell.KeyCtrlD, 0, tcell.ModNone)
			if err := s.waitRun(); err != nil {
				t.Fatal(err)
			}
			select {
			case action := <-s.actions:
				if action.Type != "QUIT" {
					t.Fatalf("action = %s", action.Type)
				}
			default:
				t.Fatal("Ctrl+D did not request a client shutdown (Ctrl+C may have stopped tview)")
			}
		})
	}
}

func TestInputFeedbackIsSelectiveAndRateLimited(t *testing.T) {
	ui := newIdleTUI(t, 32)
	screen := &countingBellScreen{Screen: tcell.NewSimulationScreen("UTF-8")}
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(screen.Fini)
	screen.Screen.(tcell.SimulationScreen).SetSize(140, 40)
	ui.captureScreen(screen)
	ui.applyLayout(screen)
	check := func(name string, want int, action func()) {
		t.Helper()
		ui.lastBell = time.Time{}
		before := screen.bells
		action()
		if got := screen.bells - before; got != want {
			t.Fatalf("%s: bells = %d, want %d", name, got, want)
		}
	}
	capture := ui.input.GetInputCapture()
	check("history boundary", 1, func() {
		for range 20 {
			capture(tcell.NewEventKey(tcell.KeyCtrlP, 0, tcell.ModNone))
		}
	})
	ui.rememberInput("hello")
	check("valid history", 0, func() {
		capture(tcell.NewEventKey(tcell.KeyCtrlP, 0, tcell.ModNone))
		capture(tcell.NewEventKey(tcell.KeyCtrlN, 0, tcell.ModNone))
	})
	check("ordinary typing", 0, func() { ui.handleKey(tcell.NewEventKey(tcell.KeyRune, 'a', tcell.ModNone)) })
	check("background errors", 0, func() { ui.applyEvent(client.DisplayEvent{Type: "ERROR", Content: "relay disconnected"}) })
	check("unknown command", 1, func() { ui.handleCommand("/typo") })
	if !strings.Contains(strings.Join(ui.snapshot().logs.lines, "\n"), "Unknown command") {
		t.Fatal("unknown command has no visible explanation")
	}
	check("missing join argument", 1, func() { ui.handleCommand("/join") })
	check("unknown theme", 1, func() { ui.handleCommand("/theme typo") })
	ui.setFocus(ui.outputView)
	check("typing into messages", 1, func() { ui.handleKey(tcell.NewEventKey(tcell.KeyRune, 'a', tcell.ModNone)) })
	check("scrolling messages", 0, func() { ui.handleKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone)) })
	check("native TextView navigation", 0, func() {
		for _, r := range "gGhjkl" {
			ev := tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone)
			if ui.handleKey(ev) != ev || ui.handleMaximizedViewKeys(ev) != ev {
				t.Fatalf("native scroll key %q was swallowed", r)
			}
		}
	})
	check("length limit", 1, func() {
		ui.input.SetText(strings.Repeat("a", client.MaxMsgLen))
		ui.input.InputHandler()(tcell.NewEventKey(tcell.KeyRune, 'a', tcell.ModNone), func(tview.Primitive) {})
		if len(ui.input.GetText()) != client.MaxMsgLen {
			t.Fatal("overlong input accepted")
		}
	})
	check("normal input accepted", 0, func() {
		ui.input.SetText("hell")
		ui.input.InputHandler()(tcell.NewEventKey(tcell.KeyRune, 'o', tcell.ModNone), func(tview.Primitive) {})
		if ui.input.GetText() != "hello" {
			t.Fatal("normal input rejected")
		}
	})
	ui.lastBell = time.Now().Add(-time.Second)
	before := screen.bells
	ui.bell()
	if screen.bells != before+1 {
		t.Fatal("bell did not recover after cooldown")
	}
	ui.actionsChan = make(chan client.UserAction)
	check("rejected explicit action", 1, func() { ui.submit(client.UserAction{Type: "SEND_MESSAGE", Payload: "retry later"}) })
	check("best-effort completion", 0, func() { ui.submit(client.UserAction{Type: "REQUEST_NICK_COMPLETION", Payload: "a"}) })
	check("best-effort shutdown", 0, func() { ui.submit(client.UserAction{Type: "QUIT"}) })
	check("empty chat activation", 1, func() { ui.handleChatListKeys(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)) })
	check("empty chat navigation", 0, func() { ui.handleChatListKeys(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone)) })
}

func TestCtrlDQuitsWhenClientQueueIsFull(t *testing.T) {
	ui := newIdleTUI(t, 0)
	ui.handleKey(tcell.NewEventKey(tcell.KeyCtrlD, 0, tcell.ModNone))
	if !ui.isQuitRequested() {
		t.Fatal("full client queue prevented exit")
	}
}
