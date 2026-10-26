package client

import (
	"testing"

	"fiatjaf.com/nostr"
)

func TestMessageRecipientMetadataChanges(t *testing.T) {
	c, events := testClient(t)
	c.config.Views = []View{{Name: "group", IsGroup: true, Children: []string{"lobby", "coffee"}}}
	c.config.ActiveViewName = "group"
	ev := signedEvent(t, "first")
	for _, step := range []struct {
		nick, chat        string
		forget, unchanged bool
	}{
		{"alice", "lobby", false, false},
		{"alice", "lobby", false, true},
		{"renamed", "lobby", false, false},
		{"renamed", "coffee", false, false},
		{"renamed", "coffee", false, true},
		{"renamed", "coffee", true, false},
	} {
		ev.Content += "."
		ev.Tags = nostr.Tags{{"d", step.chat}, {"n", step.nick}}
		if err := ev.Sign(nostr.MustSecretKeyFromHex(signerTestKeys[0])); err != nil {
			t.Fatal(err)
		}
		if step.forget {
			c.userContext.Remove(ev.PubKey.Hex())
		}
		c.processEvent(ev, "local")
		select {
		case display := <-events:
			if display.RecipientUnchanged != step.unchanged {
				t.Fatalf("nick=%q chat=%q forget=%v: unchanged=%v, want %v", step.nick, step.chat, step.forget, display.RecipientUnchanged, step.unchanged)
			}
		default:
			t.Fatal("valid message was not displayed")
		}
	}
}
