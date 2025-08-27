// package tui is responsible for creating, rendering, and managing all aspects
// of the Terminal User Interface (TUI).
package tui

import (
	"fmt"
	"strchat-tui/client"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// TUI is the main struct that holds all TUI components and channels.
type TUI struct {
	app         *tview.Application
	output      *tview.TextView
	input       *tview.InputField
	chatInput   *tview.InputField
	logs        *tview.TextView
	actionsChan chan<- client.UserAction
}

// New creates and initializes the entire TUI application,
// setting up views, handlers, and the main layout.
func New(actions chan<- client.UserAction, events <-chan client.DisplayEvent) *TUI {
	// --- Global Color Theme ---
	tview.Styles.PrimitiveBackgroundColor = tcell.ColorBlack // Main background
	tview.Styles.PrimaryTextColor = tcell.ColorGainsboro     // Primary text color (a light grey, softer than pure white)
	tview.Styles.BorderColor = tcell.ColorDarkOliveGreen     // Border color (a dim green)
	tview.Styles.TitleColor = tcell.ColorLimeGreen           // Title color (a bright, accent green)

	t := &TUI{
		app:         tview.NewApplication(),
		actionsChan: actions,
	}

	// Create and configure all the visual components (views).
	t.setupViews()

	// Set up handlers for user input and global key events.
	t.setupHandlers()

	// Start a goroutine to listen for events from the client.
	go t.listenForEvents(events)

	return t
}

// setupViews creates and configures all the visual primitives of the TUI.
func (t *TUI) setupViews() {
	// --- Chat Window ---
	t.output = tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true)

	// --- Log Window ---
	t.logs = tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true)
	t.logs.SetBorder(true)

	// --- Input Fields ---
	t.input = tview.NewInputField().
		SetAcceptanceFunc(tview.InputFieldMaxLength(280)).
		SetFieldBackgroundColor(tcell.NewRGBColor(0, 40, 0)). // Input field background (a very dark green)
		SetFieldTextColor(tcell.ColorLime)                    // Input field text color (a bright green)

	t.chatInput = tview.NewInputField().
		SetLabelStyle(tcell.StyleDefault.Foreground(tcell.ColorLimeGreen)). // Color of the "#" label (a bright green)
		SetLabel("#").
		SetText("21m").
		SetAcceptanceFunc(tview.InputFieldMaxLength(20)).
		SetFieldBackgroundColor(tcell.NewRGBColor(0, 40, 0)). // Input field background (a very dark green)
		SetFieldTextColor(tcell.ColorLime)                    // Input field text color (a bright green)
	t.chatInput.SetBorderPadding(0, 0, 1, 0)

	bottomFlex := tview.NewFlex().
		SetDirection(tview.FlexColumn).
		AddItem(t.input, 0, 1, true).
		AddItem(t.chatInput, 12, 0, false)
	bottomFlex.SetBorder(true).
		SetBorderColor(tcell.ColorLimeGreen) // Border around the input fields (a bright green)

	// --- Main Layout ---
	mainFlex := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(t.logs, 12, 0, false).
		AddItem(t.output, 0, 1, false).
		AddItem(bottomFlex, 3, 0, true)

	t.app.SetRoot(mainFlex, true).SetFocus(t.input)
}

// setupHandlers defines the behavior for user interactions, such as sending messages
// and handling global key presses like Tab and Ctrl+C.
func (t *TUI) setupHandlers() {
	// Handler for sending a message.
	t.input.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEnter {
			message := strings.TrimSpace(t.input.GetText())
			if message == "" {
				return
			}
			if message == "/quit" {
				t.app.Stop()
				return
			}
			t.actionsChan <- client.UserAction{Type: "SEND_MESSAGE", Payload: message}
			t.input.SetText("")
			t.output.ScrollToEnd()
		}
	})

	// Handler for switching the chat room.
	t.chatInput.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEnter {
			chat := strings.TrimSpace(t.chatInput.GetText())
			if chat == "" {
				chat = "21m"
				t.chatInput.SetText(chat)
			}
			t.actionsChan <- client.UserAction{Type: "SWITCH_CHAT", Payload: chat}
			t.app.SetFocus(t.input)
		}
	})

	// Global input handler for focus switching (Tab) and quitting (Ctrl+C).
	t.app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyCtrlC {
			t.actionsChan <- client.UserAction{Type: "QUIT"}
			return nil // Consume the event.
		}
		if event.Key() == tcell.KeyTab {
			switch t.app.GetFocus() {
			case t.input:
				t.app.SetFocus(t.chatInput)
			case t.chatInput:
				t.app.SetFocus(t.output)
			case t.output:
				t.app.SetFocus(t.logs)
			case t.logs:
				t.app.SetFocus(t.input)
			}
			return nil // Consume the event.
		}
		return event // Pass other events through.
	})
}

// listenForEvents runs in a separate goroutine, listening for events from the
// client and updating the TUI accordingly.
func (t *TUI) listenForEvents(events <-chan client.DisplayEvent) {
	for event := range events {
		// Use QueueUpdateDraw to safely update the TUI from a different goroutine.
		t.app.QueueUpdateDraw(func() {
			switch event.Type {
			case "NEW_MESSAGE":
				trimmedURL := strings.TrimPrefix(event.RelayURL, "wss://")
				fmt.Fprintf(t.output, "[grey][%s][-] <%s%s#%s[-]> %s [grey](%s %s)[-]\n",
					event.Timestamp, event.Color, event.Nick, event.PubKey, event.Content, event.ID, trimmedURL)
			case "STATUS":
				fmt.Fprintf(t.logs, "[yellow][%s] %s[-]\n", time.Now().Format("15:04:05"), event.Content)
				// Smart autoscroll: only scroll if the user isn't actively looking at the logs.
				if t.app.GetFocus() != t.logs {
					t.logs.ScrollToEnd()
				}
			case "ERROR":
				fmt.Fprintf(t.logs, "[red][%s] ERROR: %s[-]\n", time.Now().Format("15:04:05"), event.Content)
				if t.app.GetFocus() != t.logs {
					t.logs.ScrollToEnd()
				}
			}
		})
	}
	// This will be reached when the events channel is closed by the client.
	t.app.Stop()
}

// Run starts the TUI application's main event loop.
func (t *TUI) Run() error {
	return t.app.Run()
}
