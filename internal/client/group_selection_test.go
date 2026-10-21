package client

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestGroupSelectionSurvivesConfigReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeTestFile(t, path, `{"views":[
		{"name":"mixed","is_group":true,"children":["coffee","lobby"],"selected_chat":"lobby"},
		{"name":"coffee"},{"name":"lobby"}
	],"active_view_name":"mixed"}`, 0o600)
	conf, err := loadConfigFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	conf.Nick = "updated"
	if err := conf.save(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := loadConfigFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(reloaded.Views)
	if err != nil {
		t.Fatal(err)
	}
	var views []struct {
		Name         string `json:"name"`
		SelectedChat string `json:"selected_chat"`
	}
	if err := json.Unmarshal(data, &views); err != nil {
		t.Fatal(err)
	}
	if views[0].SelectedChat != "lobby" {
		t.Fatalf("saved group choice was lost after reload: %q", views[0].SelectedChat)
	}
}

func TestGroupSelectionDefaultsAndStaleValues(t *testing.T) {
	for _, tc := range []struct{ name, saved, want string }{
		{"legacy", "", "coffee"},
		{"removed member", "gone", "coffee"},
		{"saved member", "lobby", "lobby"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			data, err := json.Marshal(&config{Views: []View{
				{Name: "mixed", IsGroup: true, Children: []string{"coffee", "lobby"}, SelectedChat: tc.saved},
				{Name: "coffee", SelectedChat: "lobby"}, {Name: "lobby"},
			}})
			if err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, path, string(data), 0o600)
			conf, err := loadConfigFrom(path)
			if err != nil {
				t.Fatal(err)
			}
			if conf.Views[0].SelectedChat != tc.want || conf.Views[1].SelectedChat != "" {
				t.Fatalf("invalid selection after load: group=%q standalone=%q", conf.Views[0].SelectedChat, conf.Views[1].SelectedChat)
			}
			conf.Views[0].Children = []string{"lobby", "devchat"}
			if err := conf.save(); err != nil {
				t.Fatal(err)
			}
			reloaded, err := loadConfigFrom(path)
			if err != nil {
				t.Fatal(err)
			}
			if reloaded.Views[0].SelectedChat != "lobby" {
				t.Fatalf("membership change did not persist a valid selection: %q", reloaded.Views[0].SelectedChat)
			}
		})
	}
}

func TestSelectGroupChatPersistsEachGroupWithoutActivatingIt(t *testing.T) {
	c, events := testClient(t)
	c.config.path = filepath.Join(t.TempDir(), "config.json")
	c.config.Nick = "tester"
	c.config.Views = []View{
		{Name: "mixed", IsGroup: true, Children: []string{"coffee", "lobby", "devchat"}},
		{Name: "other", IsGroup: true, Children: []string{"coffee", "devchat"}, SelectedChat: "devchat"},
		{Name: "coffee"}, {Name: "lobby"}, {Name: "devchat"},
	}
	c.config.ActiveViewName = "coffee"
	if err := c.config.save(); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"mixed": "coffee", "other": "devchat"}
	for _, action := range []UserAction{
		{Type: "SELECT_GROUP_CHAT", View: "mixed", Chat: "lobby"},
		{Type: "SELECT_GROUP_CHAT", View: "other", Chat: "coffee"},
		{Type: "SELECT_GROUP_CHAT", View: "mixed", Chat: "devchat"},
	} {
		c.handleAction(action)
		want[action.View] = action.Chat
		reloaded, err := loadConfigFrom(c.config.path)
		if err != nil {
			t.Fatal(err)
		}
		for _, view := range reloaded.Views {
			if view.IsGroup && view.SelectedChat != want[view.Name] {
				t.Fatalf("group %q saved %q, want %q", view.Name, view.SelectedChat, want[view.Name])
			}
		}
		found := false
		for len(events) > 0 {
			ev := <-events
			if ev.Type == "STATE_UPDATE" {
				for _, view := range ev.Payload.(StateUpdate).Views {
					if view.Name == action.View && view.SelectedChat == action.Chat {
						found = true
					}
				}
			}
		}
		if !found {
			t.Fatal("saved choice was not included in the state snapshot")
		}
		if reloaded.ActiveViewName != "coffee" || len(c.relays) != 0 {
			t.Fatal("choosing a conversation changed the active view or subscriptions")
		}
	}
}
