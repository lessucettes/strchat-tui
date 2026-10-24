package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/lessucettes/strchat-tui/internal/client"
)

func TestClearForgetsModelWidgetsAndInputWithoutWaitingForClient(t *testing.T) {
	ui := newIdleTUI(t, 8)
	actions := make(chan client.UserAction, 8)
	ui.actionsChan = actions
	ui.plan = layoutPlanFor(120, 40)
	views := []client.View{{Name: "mixed", IsGroup: true, Children: []string{"lobby", "coffee"}, SelectedChat: "coffee"}}
	ui.applyEvent(client.DisplayEvent{Type: "STATE_UPDATE", Payload: client.StateUpdate{Views: views, ChatNicks: map[string]string{"coffee": "self"}}})
	ui.rememberConversation("mixed", "lobby")
	for range maxOutputLines + 10 {
		ui.output.appendChat("old message", "lobby")
	}
	for range maxLogLines + 10 {
		ui.logs.append("old log")
	}
	ui.render()
	ui.outputView.ensureWindow(80, 20)
	ui.logsView.ensureWindow(80, 20)
	ui.outputView.ScrollTo(5, 7)
	ui.logsView.ScrollTo(3, 9)
	oldLogs := ui.logs.snapshot()
	oldLogRecords := slices.Clone(ui.logsView.records)
	oldLogText := ui.logsView.TextView.GetText(false)
	oldLogFirst, oldLogLast, oldLogSynced := ui.logs.firstIndex(), ui.logs.lastIndex(), ui.logsSynced
	oldLogTop, oldLogColumn, oldLogFollow := ui.logsView.top, ui.logsView.column, ui.logsView.follow
	ui.scrollbarDrag, ui.scrollbarPress = ui.scrollbars[1], true
	if ui.outputView.GetText(false) == "" || ui.logsView.GetText(false) == "" {
		t.Fatal("fixture did not populate both rendered windows")
	}
	oldLast := ui.output.lastIndex()
	ui.rememberInput("old input")
	ui.inputDraft = "old history draft"
	ui.lastNickQuery = "old"
	ui.completionEntries = []string{"old"}
	ui.applyEvent(client.DisplayEvent{Type: "NICK_COMPLETION_RESULT", Payload: []string{"old"}})
	ui.applyEvent(client.DisplayEvent{Type: "MESSAGE_TARGET", Payload: client.MessageTarget{Text: "@old old draft"}})

	ui.handleCommand("/clear")
	select {
	case action := <-actions:
		if action.Type != "CLEAR" {
			t.Fatalf("clear submitted %s", action.Type)
		}
	default:
		t.Fatal("clear did not submit its client-side reset")
	}
	if !slices.Equal(ui.logs.snapshot(), oldLogs) || ui.logs.firstIndex() != oldLogFirst || ui.logs.lastIndex() != oldLogLast || ui.logsSynced != oldLogSynced {
		t.Fatal("clear changed log history or its sync cursor")
	}
	if !slices.Equal(ui.logsView.records, oldLogRecords) || ui.logsView.TextView.GetText(false) != oldLogText || ui.logsView.top != oldLogTop || ui.logsView.column != oldLogColumn || ui.logsView.follow != oldLogFollow {
		t.Fatal("clear changed the log widget or its scroll position")
	}
	if ui.scrollbarDrag != ui.scrollbars[1] || !ui.scrollbarPress {
		t.Fatal("clear interrupted a log scrollbar gesture")
	}
	for _, b := range []*lineBuffer{ui.output} {
		if b.count != 0 || b.bytes != 0 {
			t.Fatal("clear retained model history")
		}
		for i := range b.lines {
			if b.lines[i] != "" || b.chats[i] != "" {
				t.Fatal("empty ring still references discarded data")
			}
		}
	}
	for _, view := range []*scrollbackView{ui.outputView} {
		if len(view.records) != 0 || view.TextView.GetText(false) != "" || !view.follow || view.top != 0 || view.column != 0 {
			t.Fatal("clear retained a rendered window or scroll position")
		}
		for _, record := range view.records[:cap(view.records)] {
			if record.text != "" || record.chat != "" {
				t.Fatal("empty widget still references discarded records")
			}
		}
	}
	if len(ui.inputHistory) != 0 || ui.inputDraft != "" || len(ui.completion) != 0 || len(ui.completionEntries) != 0 || ui.lastNickQuery != "" || ui.messageTarget.Text != "" {
		t.Fatal("clear retained input, participant or recipient data")
	}
	if ui.output.firstIndex() != oldLast+1 || ui.conversationChat(ui.snapshot()) != "lobby" || ui.chatNicks["coffee"] != "self" || len(ui.views) != 1 {
		t.Fatal("clear changed the monotonic history cursor, group destination or chat state")
	}

	// Buffered pre-clear results cannot refill the screen. A later acknowledgement
	// must not clear text the user typed while the client handled the reset.
	ui.input.SetText("new draft")
	ui.applyEvent(client.DisplayEvent{Type: "INFO", Content: "stale command output"})
	ui.applyEvent(client.DisplayEvent{Type: "NEW_MESSAGE", Chat: "lobby", Content: "stale message"})
	ui.applyEvent(client.DisplayEvent{Type: "WARN", Content: "Warning during clear."})
	ui.applyEvent(client.DisplayEvent{Type: "NICK_COMPLETION_RESULT", Payload: []string{"old"}})
	ui.applyEvent(client.DisplayEvent{Type: "MESSAGE_TARGET", Payload: client.MessageTarget{Text: "old draft"}})
	if ui.output.count != 0 || len(ui.completion) != 0 || ui.messageTarget.Text != "" {
		t.Fatal("buffered events resurrected cleared data")
	}
	if !strings.Contains(strings.Join(ui.logs.snapshot(), "\n"), "Warning during clear.") {
		t.Fatal("pending clear discarded an operational warning")
	}
	ui.applyEvent(client.DisplayEvent{Type: "CLEARED"})
	ui.applyEvent(client.DisplayEvent{Type: "INFO", Content: "new output"})
	ui.render()
	ui.outputView.ensureWindow(80, 20)
	if !strings.Contains(ui.outputView.GetText(false), "new output") || ui.input.GetText() != "new draft" {
		t.Fatal("clear acknowledgement lost new input or failed to resume output")
	}
}

func TestClearRepeatedAndRejectedRequests(t *testing.T) {
	ui := newIdleTUI(t, 2)
	actions := make(chan client.UserAction, 2)
	ui.actionsChan = actions
	ui.handleCommand("/clear")
	ui.handleCommand("/clear")
	ui.applyEvent(client.DisplayEvent{Type: "CLEARED"})
	ui.applyEvent(client.DisplayEvent{Type: "INFO", Content: "between clears"})
	if ui.output.count != 0 {
		t.Fatal("first acknowledgement released the second clear boundary")
	}
	ui.applyEvent(client.DisplayEvent{Type: "CLEARED"})
	ui.applyEvent(client.DisplayEvent{Type: "INFO", Content: "keep this"})
	// Both actions are still in the saturated input queue. A failed request must
	// retain the current history, not clear only one half of the application.
	ui.handleCommand("/clear")
	if ui.output.count != 1 || !strings.Contains(ui.output.snapshot()[0], "keep this") {
		t.Fatal("rejected clear discarded history")
	}
	<-actions
	<-actions
	ui.handleCommand("/clear extra")
	if len(ui.actionsChan) != 0 || ui.output.count != 1 {
		t.Fatal("clear with unexpected arguments changed the session")
	}
}

func TestClearPreservesNewLocalDiagnosticsWhileClientCatchesUp(t *testing.T) {
	ui := newIdleTUI(t, 1)
	ui.handleCommand("/clear")
	ui.handleCommand("/clear extra")
	ui.handleCommand("/join")
	if ui.logs.count != 2 || !strings.Contains(strings.Join(ui.logs.snapshot(), "\n"), "Usage: /clear") {
		t.Fatal("pending clear swallowed new local usage errors")
	}
	ui.handleCommand("/theme")
	if ui.output.count != 1 {
		t.Fatal("pending clear swallowed local command output")
	}
	ui.handleCommand("/clear") // The queue is full; retain both the data and warning.
	if ui.logs.count != 3 || ui.output.count != 1 {
		t.Fatal("rejected clear lost local feedback or discarded data")
	}
	beforeLogs := ui.logs.count
	levels := []string{"STATUS", "WARN", "ERROR"}
	for _, level := range levels {
		ui.applyEvent(client.DisplayEvent{Type: level, Content: "Operational event during clear."})
	}
	ui.appendLogLine("Captured logger event during clear.")
	ui.applyEvent(client.DisplayEvent{Type: "INFO", Content: "stale client output"})
	if ui.logs.count != beforeLogs+len(levels)+1 || ui.output.count != 1 {
		t.Fatal("clear discarded log events or restored stale message output")
	}
}
