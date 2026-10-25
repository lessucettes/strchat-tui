package client

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectGroupChatRejectsInvalidTargetsWithoutSaving(t *testing.T) {
	c, events := testClient(t)
	t.Cleanup(c.shutdown)
	c.config.path = filepath.Join(t.TempDir(), "config.json")
	c.config.AnchorRelays = []string{"ws://127.0.0.1:1"}
	c.config.Views = []View{
		{Name: "mixed", IsGroup: true, Children: []string{"coffee", "lobby"}},
		{Name: "coffee"}, {Name: "lobby"}, {Name: "other"},
	}
	c.config.ActiveViewName = "lobby"
	if err := c.config.save(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(c.config.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []UserAction{
		{Type: "SELECT_GROUP_CHAT", View: "missing", Chat: "coffee"},
		{Type: "SELECT_GROUP_CHAT", View: "coffee", Chat: "lobby"},
		{Type: "SELECT_GROUP_CHAT", View: "mixed", Chat: "missing"},
		{Type: "SELECT_GROUP_CHAT", View: "mixed"},
		{Type: "ACTIVATE_VIEW", Payload: "missing", Chat: "coffee"},
		{Type: "ACTIVATE_VIEW", Payload: "coffee", Chat: "lobby"},
		{Type: "ACTIVATE_VIEW", Payload: "mixed", Chat: "other"},
	} {
		c.handleAction(action)
		select {
		case ev := <-events:
			if ev.Type != "ERROR" {
				t.Fatalf("invalid selection emitted %q", ev.Type)
			}
		default:
			t.Fatal("invalid selection was not reported")
		}
		after, err := os.ReadFile(c.config.path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) || c.config.ActiveViewName != "lobby" || c.config.Views[0].SelectedChat != "coffee" {
			t.Fatal("invalid selection changed the configuration")
		}
	}
}

func TestGroupSelectionSaveFailureIsVisibleAndSessionOnly(t *testing.T) {
	c, events := testClient(t)
	path := filepath.Join(t.TempDir(), "config.json")
	c.config.path, c.config.Nick = path, "tester"
	c.config.Views = []View{{Name: "mixed", IsGroup: true, Children: []string{"coffee", "lobby"}}}
	c.config.ActiveViewName = "mixed"
	if err := c.config.save(); err != nil {
		t.Fatal(err)
	}
	// Renaming a file over a directory fails even when tests run as root.
	c.config.path = t.TempDir()
	c.handleAction(UserAction{Type: "SELECT_GROUP_CHAT", View: "mixed", Chat: "lobby"})
	if c.config.Views[0].SelectedChat != "lobby" {
		t.Fatal("failed persistence discarded the valid session choice")
	}
	found := false
	for len(events) > 0 {
		ev := <-events
		if ev.Type == "ERROR" && strings.Contains(ev.Content, "current selection remains active") && strings.Contains(ev.Content, "may not survive restart") && strings.Contains(ev.Content, "Reason:") {
			found = true
		}
	}
	if !found {
		t.Fatal("failed persistence was not explicitly reported")
	}
	reloaded, err := loadConfigFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Views[0].SelectedChat != "coffee" {
		t.Fatal("failed save changed the last valid saved choice")
	}
}

func TestLeavingChosenMemberPersistsFallbackAndGroupDeletionRemovesChoice(t *testing.T) {
	c, _ := testClient(t)
	t.Cleanup(c.shutdown)
	c.config.path = filepath.Join(t.TempDir(), "config.json")
	c.config.Nick = "tester"
	c.config.AnchorRelays = []string{"ws://127.0.0.1:1"}
	c.config.Views = []View{
		{Name: "mixed", IsGroup: true, Children: []string{"coffee", "lobby", "devchat"}, SelectedChat: "lobby"},
		{Name: "coffee"}, {Name: "lobby"}, {Name: "devchat"},
	}
	c.config.ActiveViewName = "mixed"
	c.leaveChat("lobby")
	reloaded, err := loadConfigFrom(c.config.path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Views[0].SelectedChat != "coffee" {
		t.Fatal("removing the chosen member did not persist the fallback")
	}
	c.deleteGroup("mixed")
	reloaded, err = loadConfigFrom(c.config.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range reloaded.Views {
		if view.Name == "mixed" || view.SelectedChat != "" {
			t.Fatal("deleting the group left a persisted selection")
		}
	}
}
