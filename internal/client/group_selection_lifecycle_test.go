package client

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestGroupCreationUsesDeterministicNamesAndPreservesCommands(t *testing.T) {
	for _, tc := range []struct{ payload, want string }{
		{"coffee,lobby", "meli-kiwen-pan"},
		{"lobby, coffee, lobby", "meli-kiwen-pan"},
		{"ab,c", "weka-pona-suno"},
		{"a,bc", "mun-mute-tenpo"},
	} {
		t.Run(tc.payload, func(t *testing.T) {
			c, events := testClient(t)
			t.Cleanup(c.shutdown)
			c.config.path = filepath.Join(t.TempDir(), "config.json")
			c.config.AnchorRelays = []string{"ws://127.0.0.1:1"}
			c.config.Views = []View{{Name: "coffee"}, {Name: "lobby"}, {Name: "ab"}, {Name: "c"}, {Name: "a"}, {Name: "bc"}}
			c.handleAction(UserAction{Type: "CREATE_GROUP", Payload: tc.payload})
			if len(c.config.Views) != 7 || c.config.ActiveViewName != tc.want {
				t.Fatalf("created group = %+v, active = %q, want %q", c.config.Views, c.config.ActiveViewName, tc.want)
			}
			group := c.config.Views[6]
			if !group.IsGroup || len(group.Children) != 2 || !slices.IsSorted(group.Children) {
				t.Fatalf("invalid group: %+v", group)
			}
			c.handleAction(UserAction{Type: "ACTIVATE_VIEW", Payload: tc.want, Chat: group.Children[1]})
			reloaded, err := loadConfigFrom(c.config.path)
			if err != nil {
				t.Fatal(err)
			}
			if reloaded.ActiveViewName != tc.want || reloaded.Views[6].Name != tc.want || reloaded.Views[6].SelectedChat != group.Children[1] {
				t.Fatalf("group name or selection did not survive reload: %+v", reloaded)
			}
			c.config = reloaded
			for len(events) > 0 {
				<-events
			}
			c.handleAction(UserAction{Type: "LIST_CHATS"})
			if ev := <-events; ev.Type != "INFO" || !strings.Contains(ev.Content, tc.want+" (Group)") {
				t.Fatalf("group missing from /list: %+v", ev)
			}
			c.handleAction(UserAction{Type: "DELETE_VIEW", Payload: tc.want})
			if len(c.config.Views) != 6 || c.getActiveView().IsGroup {
				t.Fatal("/del did not remove the named group and activate a remaining chat")
			}
		})
	}
}

func TestGroupCreationResolvesNameCollisions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing View
		payload  string
		want     string
	}{
		// These different member sets produce the same initial FNV-1a name.
		{"group", View{Name: "nimi-monsi-ijo", IsGroup: true, Children: []string{"coffee", "room-148"}},
			"coffee,room-2123", "noka-monsi-ijo"},
		{"chat", View{Name: "luka-lupa-ko"}, "coffee,room-18", "lukin-lupa-ko"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, events := testClient(t)
			t.Cleanup(c.shutdown)
			c.config.path = filepath.Join(t.TempDir(), "config.json")
			c.config.AnchorRelays = []string{"ws://127.0.0.1:1"}
			c.config.Views = []View{{Name: "coffee"}, {Name: "room-18"}, {Name: "room-148"}, {Name: "room-2123"}, tc.existing}
			c.handleAction(UserAction{Type: "CREATE_GROUP", Payload: tc.payload})
			if len(c.config.Views) != 6 || c.config.ActiveViewName != tc.want || c.config.Views[4].Name != tc.existing.Name || !slices.Equal(c.config.Views[4].Children, tc.existing.Children) {
				t.Fatalf("collision did not preserve both views: %+v", c.config)
			}
			reloaded, err := loadConfigFrom(c.config.path)
			if err != nil {
				t.Fatal(err)
			}
			if reloaded.Views[5].Name != tc.want || reloaded.ActiveViewName != tc.want {
				t.Fatalf("resolved name did not survive reload: %+v", reloaded)
			}
			c.config = reloaded
			for len(events) > 0 {
				<-events
			}
			c.handleAction(UserAction{Type: "CREATE_GROUP", Payload: tc.payload})
			if ev := <-events; ev.Type != "ERROR" || !strings.Contains(ev.Content, "already exists") || !strings.Contains(ev.Content, tc.want) || len(c.config.Views) != 6 {
				t.Fatalf("recreating a collision-resolved group was not treated as a duplicate: %+v", ev)
			}
		})
	}
}

func TestGroupNamesRejectDuplicateMembershipAndAmbiguousJoinsWithoutSaving(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing View
		action   UserAction
		want     string
	}{
		{"legacy duplicate", View{Name: "Group-240dec", IsGroup: true, Children: []string{"lobby", "coffee"}},
			UserAction{Type: "CREATE_GROUP", Payload: "coffee,lobby"}, "already exists"},
		{"join collision", View{Name: "luka-lupa-ko", IsGroup: true, Children: []string{"coffee", "room-18"}},
			UserAction{Type: "JOIN_CHATS", Payload: "luka-lupa-ko"}, "Name is already used"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, events := testClient(t)
			t.Cleanup(c.shutdown)
			c.config.path = filepath.Join(t.TempDir(), "config.json")
			c.config.AnchorRelays = []string{"ws://127.0.0.1:1"}
			c.config.Views = []View{{Name: "coffee"}, {Name: "lobby"}, {Name: "room-18"}, {Name: "room-148"}, {Name: "room-2123"}, tc.existing}
			if err := c.config.save(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(c.config.path)
			if err != nil {
				t.Fatal(err)
			}
			c.handleAction(tc.action)
			if ev := <-events; ev.Type != "ERROR" || !strings.Contains(ev.Content, tc.want) || !strings.Contains(ev.Content, tc.existing.Name) {
				t.Fatalf("duplicate or collision not reported correctly: %+v", ev)
			}
			after, err := os.ReadFile(c.config.path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) || len(c.config.Views) != 6 || c.config.ActiveViewName != "lobby" {
				t.Fatal("rejected operation changed the views, active selection or saved configuration")
			}
		})
	}
}

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
