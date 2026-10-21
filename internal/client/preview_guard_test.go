package client

import (
	"strings"
	"testing"
)

func TestSendRefusesDestinationChangedSincePreview(t *testing.T) {
	c, events := groupDestinationClient(t, 0)
	pk := strings.Repeat("a", 60) + "beef"
	c.userContext.Add(pk, userContext{nick: "alice", shortPubKey: "beef", chat: "lobby"})
	c.previewMessageTarget("@alice#beef hello", "group", "lobby")
	preview := (<-events).Payload.(MessageTarget)
	if preview.Chat != "lobby" || preview.Error != "" {
		t.Fatalf("invalid preview fixture: %+v", preview)
	}
	// The same public key can next be seen in another member chat. Do not
	// silently send there when the composer displayed the previous destination.
	c.userContext.Add(pk, userContext{nick: "alice", shortPubKey: "beef", chat: "u33dc"})
	c.handleAction(UserAction{Type: "SEND_MESSAGE", Payload: preview.Text, View: "group", Chat: "lobby", ExpectedChat: preview.Chat})
	expectNoJob(t, c)
	if got := onlyError(t, events); !strings.Contains(got, "destination changed") {
		t.Fatalf("unexpected route-change refusal: %q", got)
	}
}
