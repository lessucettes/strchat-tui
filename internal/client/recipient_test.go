package client

import (
	"strings"
	"testing"
)

func TestReplyRejectsAmbiguousRecipient(t *testing.T) {
	for _, group := range []bool{false, true} {
		t.Run(map[bool]string{false: "chat", true: "group"}[group], func(t *testing.T) {
			c, events := testClient(t)
			if group {
				c.config.Views = []View{{Name: "group", IsGroup: true, Children: []string{"lobby", "other"}}}
				c.config.ActiveViewName = "group"
			}
			for _, pk := range []string{strings.Repeat("a", 60) + "beef", strings.Repeat("b", 60) + "beef"} {
				c.userContext.Add(pk, userContext{nick: "alice", shortPubKey: "beef", chat: "lobby"})
			}
			c.relays["local"] = &managedRelay{connected: true, chats: []string{"lobby"}}
			c.publishMessage("@alice#beef hello")
			if len(c.outgoing) != 0 {
				t.Fatal("ambiguous recipient was selected by cache order")
			}
			if len(events) != 1 {
				t.Fatal("ambiguous reply must report an error")
			}
			if event := <-events; event.Type != "ERROR" || !strings.Contains(event.Content, "Ambiguous") {
				t.Fatalf("unexpected reply result: %+v", event)
			}
		})
	}
}

func TestReplyIgnoresMatchingRecipientOutsideActiveChat(t *testing.T) {
	c, _ := testClient(t)
	inside := strings.Repeat("a", 60) + "beef"
	outside := strings.Repeat("b", 60) + "beef"
	c.userContext.Add(outside, userContext{nick: "alice", shortPubKey: "beef", chat: "other"})
	c.userContext.Add(inside, userContext{nick: "alice", shortPubKey: "beef", chat: "lobby"})
	c.relays["local"] = &managedRelay{connected: true, chats: []string{"lobby"}}
	c.publishMessage("@alice#beef hello")
	select {
	case job := <-c.outgoing:
		if job.chat != "lobby" || job.event.Tags.Find("p")[1] != inside {
			t.Fatal("reply selected a recipient outside the active view")
		}
	default:
		t.Fatal("an out-of-view collision prevented the valid reply")
	}
}

func TestCompletionDisambiguatesRecipients(t *testing.T) {
	c, events := testClient(t)
	c.config.Views = []View{{Name: "group", IsGroup: true, Children: []string{"lobby", "other"}}}
	c.config.ActiveViewName = "group"
	c.ensureViewIdentities(c.getActiveView())
	keys := []string{strings.Repeat("a", 60) + "beef", strings.Repeat("b", 60) + "beef"}
	for i, chat := range []string{"lobby", "other"} {
		c.userContext.Add(keys[i], userContext{nick: "alice", shortPubKey: "beef", chat: chat})
	}
	c.handleNickCompletion("@ali")
	entries := (<-events).Payload.([]string)
	if len(entries) != 2 || entries[0] == entries[1] {
		t.Fatalf("ambiguous completion entries: %q", entries)
	}
	c.relays["local"] = &managedRelay{connected: true, chats: []string{"lobby", "other"}}
	for i, entry := range entries {
		if entry != "@alice#"+keys[i]+" " {
			t.Fatalf("completion = %q, want full public key", entry)
		}
		c.publishMessage(entry + "hello")
		select {
		case job := <-c.outgoing:
			if job.event.Tags.Find("p")[1] != keys[i] {
				t.Fatal("completed reply targets the wrong recipient")
			}
		default:
			t.Fatal("completed full-key reply was not queued")
		}
	}
}
