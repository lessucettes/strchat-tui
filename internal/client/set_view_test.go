package client

import (
	"os"
	"path/filepath"
	"testing"
)

func TestActivateGroupConversationValidatesBeforeChangingView(t *testing.T) {
	c, events := testClient(t)
	t.Cleanup(c.shutdown)
	c.config.path = filepath.Join(t.TempDir(), "config.json")
	c.config.AnchorRelays = []string{"ws://127.0.0.1:1"}
	c.config.Views = []View{
		{Name: "mixed", IsGroup: true, Children: []string{"coffee", "lobby"}, SelectedChat: "coffee"},
		{Name: "coffee"}, {Name: "lobby"}, {Name: "other"},
	}
	c.config.ActiveViewName = "other"
	if err := c.config.save(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(c.config.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []UserAction{
		{Type: "ACTIVATE_VIEW", Payload: "missing", Chat: "coffee"},
		{Type: "ACTIVATE_VIEW", Payload: "coffee", Chat: "lobby"},
		{Type: "ACTIVATE_VIEW", Payload: "mixed", Chat: "other"},
	} {
		c.handleAction(action)
		select {
		case event := <-events:
			if event.Type != "ERROR" {
				t.Fatalf("invalid target emitted %s, want ERROR", event.Type)
			}
		default:
			t.Fatal("invalid target was not reported")
		}
		after, err := os.ReadFile(c.config.path)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(before) || c.config.ActiveViewName != "other" || c.config.Views[0].SelectedChat != "coffee" {
			t.Fatal("invalid conversation changed the current view, destination or saved configuration")
		}
	}

	// Avoid opening transports: selecting within an already active group must
	// update its destination without resubscribing.
	c.config.ActiveViewName = "mixed"
	c.handleAction(UserAction{Type: "ACTIVATE_VIEW", Payload: "mixed", Chat: "lobby"})
	reloaded, err := loadConfigFrom(c.config.path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.ActiveViewName != "mixed" || reloaded.Views[0].SelectedChat != "lobby" {
		t.Fatal("group activation did not persist its requested conversation")
	}
	if len(c.relays) != 0 {
		t.Fatal("selecting within the active group opened relay connections")
	}
}
