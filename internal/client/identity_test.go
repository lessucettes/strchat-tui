package client

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"fiatjaf.com/nostr"
)

func TestChatIdentityIsSharedAcrossViewsButNotChatsOrRuns(t *testing.T) {
	cfg := &config{Views: []View{
		{Name: "coffee"}, {Name: "lobby"},
		{Name: "mixed", IsGroup: true, Children: []string{"coffee", "lobby"}},
		{Name: "other-group", IsGroup: true, Children: []string{"coffee", "lobby"}},
	}, ActiveViewName: "mixed", path: filepath.Join(t.TempDir(), "config.json")}
	c := newClient(cfg, nil, make(chan DisplayEvent, 128))
	t.Cleanup(c.Stop)
	coffee, lobby := c.chatKeys["coffee"], c.chatKeys["lobby"]
	if coffee.privKey == "" || lobby.privKey == "" || coffee.pubKey == lobby.pubKey || len(c.chatKeys) != 2 {
		t.Fatal("starting in a group did not create separate member identities")
	}
	c.relays["local"] = &managedRelay{connected: true, chats: []string{"coffee", "lobby"}}
	for _, view := range []string{"coffee", "mixed", "other-group"} {
		c.setActiveView(view)
		c.publishMessageInView("hello", view, "coffee", "coffee")
		job := nextJob(t, c)
		if job.key != coffee.privKey || job.event.PubKey.Hex() != coffee.pubKey || job.event.Tags.Find("n")[1] != coffee.nick {
			t.Fatalf("view %q changed the coffee sender", view)
		}
	}
	c.setNick("alice")
	if c.chatKeys["coffee"].privKey != coffee.privKey || c.chatKeys["lobby"].privKey != lobby.privKey {
		t.Fatal("setting a nickname rotated a signing identity")
	}
	c.setNick("")
	if c.chatKeys["coffee"].nick != coffee.nick || c.chatKeys["lobby"].nick != lobby.nick {
		t.Fatal("clearing a nickname did not restore each chat's own nickname")
	}
	data, err := os.ReadFile(cfg.path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(coffee.privKey)) || bytes.Contains(data, []byte(lobby.privKey)) || bytes.Contains(data, []byte("private_key")) {
		t.Fatal("saving settings persisted signing material")
	}
	stored, err := loadConfigFrom(cfg.path)
	if err != nil {
		t.Fatal(err)
	}
	restarted := newClient(stored, nil, nil)
	t.Cleanup(restarted.Stop)
	for _, chat := range []string{"coffee", "lobby"} {
		if restarted.chatKeys[chat].pubKey == c.chatKeys[chat].pubKey {
			t.Fatalf("chat %q reused its identity after restart", chat)
		}
	}
}

func TestPublishRefusesMissingOrInvalidChatIdentity(t *testing.T) {
	for _, scenario := range []string{"missing", "empty", "mismatched"} {
		t.Run(scenario, func(t *testing.T) {
			c, events := groupDestinationClient(t, 0)
			session := c.chatKeys["lobby"]
			switch scenario {
			case "missing":
				delete(c.chatKeys, "lobby")
			case "empty":
				session.privKey = ""
				c.chatKeys["lobby"] = session
			case "mismatched":
				session.pubKey = c.chatKeys["u33dc"].pubKey
				c.chatKeys["lobby"] = session
			}
			c.publishMessageInView("hello", "group", "lobby", "lobby")
			expectNoJob(t, c)
			if got := onlyError(t, events); got == "" {
				t.Fatal("identity refusal was silent")
			}
		})
	}
}

func TestLateOwnEchoSurvivesLeavingAndRejoiningChat(t *testing.T) {
	cfg := relayTestConfig(t, "ws://127.0.0.1:1")
	c := transportClient(t, cfg)
	events := make(chan DisplayEvent, 128)
	c.eventsChan = events
	old := c.chatKeys["lobby"]
	event := signedChatEvent(t, old.privKey, ephChatKind, "lobby", "late own echo", nostr.Now())
	c.leaveChat("lobby")
	c.joinChats("lobby")
	if c.chatKeys["lobby"].pubKey == old.pubKey {
		t.Fatal("rejoining a left chat reused its old identity")
	}
	c.processEvent(event, "local")
	found := false
	for len(events) > 0 {
		display := <-events
		if display.Type == "NEW_MESSAGE" {
			found = true
			if !display.IsOwnMessage {
				t.Fatal("late signed echo lost its own-message marker")
			}
		}
	}
	if !found {
		t.Fatal("late echo was not displayed")
	}
}
