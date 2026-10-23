package client

import (
	"path/filepath"
	"testing"
)

func TestDeletingViewActivatesRemainingChatIdentity(t *testing.T) {
	for _, active := range []string{"lobby", "group"} {
		t.Run(active, func(t *testing.T) {
			c, _ := testClient(t)
			c.config.path = filepath.Join(t.TempDir(), "config.json")
			c.config.AnchorRelays = []string{"ws://127.0.0.1:1"}
			c.config.Views = []View{
				{Name: "other"},
				{Name: "lobby"},
				{Name: "group", IsGroup: true, Children: []string{"lobby", "other"}},
			}
			c.config.ActiveViewName = active
			t.Cleanup(func() { c.cancel(); c.wg.Wait() })

			c.deleteView(active)

			session, ok := c.chatKeys["other"]
			if !ok || session.privKey == "" {
				t.Fatal("fallback chat has no ephemeral identity")
			}

			// Exercise the actual publish boundary without running a socket worker.
			c.relays["local"] = &managedRelay{connected: true, chats: []string{"other"}, cancel: func() {}}
			c.publishMessage("hello from the fallback chat")
			select {
			case job := <-c.outgoing:
				if job.key != session.privKey || job.event.PubKey != session.pubKey {
					t.Fatal("fallback publish did not capture the session identity")
				}
			default:
				t.Fatal("fallback publish was not queued")
			}
			if c.config.ActiveViewName != "other" {
				t.Fatalf("active view = %q, want other", c.config.ActiveViewName)
			}
			stored, err := loadConfigFrom(c.config.path)
			if err != nil {
				t.Fatal(err)
			}
			if stored.ActiveViewName != "other" {
				t.Fatalf("saved active view = %q, want other", stored.ActiveViewName)
			}
		})
	}
}

func TestLeavingChatRemovesActiveUndersizedGroup(t *testing.T) {
	c, _ := testClient(t)
	c.config.path = filepath.Join(t.TempDir(), "config.json")
	c.config.AnchorRelays = []string{"ws://127.0.0.1:1"}
	c.config.Views = []View{
		{Name: "other"}, {Name: "lobby"},
		{Name: "group", IsGroup: true, Children: []string{"lobby", "other"}},
	}
	c.config.ActiveViewName = "group"
	t.Cleanup(func() { c.cancel(); c.wg.Wait() })
	c.leaveChat("lobby")
	if c.config.ActiveViewName != "other" || c.chatKeys["other"].privKey == "" {
		t.Fatal("removing a child left the active group pointing at a missing view")
	}
	key := c.chatKeys["other"].privKey
	c.activateRemainingView()
	if c.chatKeys["other"].privKey != key {
		t.Fatal("refreshing a surviving chat rotated its identity")
	}
	c.leaveChat("other")
	if c.getActiveView() != nil || c.config.ActiveViewName != "" || len(c.chatKeys) != 0 {
		t.Fatal("leaving the last chat retained an active view or session")
	}
}
