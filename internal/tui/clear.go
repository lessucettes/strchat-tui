package tui

import "github.com/lessucettes/strchat-tui/internal/client"

// clearSession runs on the event loop. Clear the UI immediately after the
// nonblocking submission, not after the client catches up with its input queue.
// The model lock also prevents an early CLEARED acknowledgement racing the
// pending count. Buffered message/recipient results stay suppressed until every
// accepted clear reaches the client owner; Logs and transport queues stay intact.
func (t *tui) clearSession() {
	t.mu.Lock()
	select {
	case t.actionsChan <- client.UserAction{Type: "CLEAR"}:
		t.clearPending++
	default:
		t.mu.Unlock()
		t.noteBackpressure()
		t.bell()
		return
	}
	t.output.clear()
	outputStart := t.output.firstIndex()
	t.outputSynced = t.output.lastIndex()
	t.completion = nil
	t.completionRev++
	t.messageTarget = client.MessageTarget{}
	t.recipientRev++
	t.dropped, t.dropNotice = 0, false
	t.mu.Unlock()

	// Reset the message window even when hidden. Reuse its small backing array,
	// but release text references without wrapping, recoloring or scanning text.
	t.outputView.reset(outputStart)
	t.outputView.column = 0
	t.outputView.Clear().ScrollToBeginning()

	t.inputHistory = nil
	t.historyOffset = 0
	t.inputDraft = ""
	t.lastNickQuery = ""
	t.completionEntries = nil
	t.targetQuery = client.MessageTarget{}
	t.targetQueryRev = 0
	t.targetRequested = false
	if t.scrollbarDrag != nil && t.scrollbarDrag.Primitive == t.outputView {
		t.scrollbarDrag = nil
		t.scrollbarPress = false
	}
	t.input.reset()
	t.setupInputHandlers()
	t.markDirty()
}
