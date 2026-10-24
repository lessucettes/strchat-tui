package client

import (
	"strings"
	"testing"
)

func TestHelpListsCommandsWithoutPaneNavigation(t *testing.T) {
	c, events := testClient(t)
	c.getHelp("")
	event := <-events
	if event.Type != "INFO" || !strings.HasPrefix(event.Content, "COMMANDS:\n") {
		t.Fatalf("unexpected command help: %+v", event)
	}
	for _, command := range []string{"/join", "/set", "/quit"} {
		if !strings.Contains(event.Content, command) {
			t.Errorf("command help is missing %s", command)
		}
	}
	for _, category := range []string{"chats", "moderation", "appearance", "network", "general"} {
		if !strings.Contains(event.Content, strings.ToUpper(category)+":\n") {
			t.Errorf("command help is missing category %s", category)
		}
	}
	if strings.Contains(event.Content, "* Groups:") || strings.Contains(event.Content, "Alt+N") {
		t.Fatal("command help includes unrelated pane navigation instructions")
	}
}

func TestHelpCategorySelectsOnlyRequestedCommands(t *testing.T) {
	for _, tc := range []struct{ category, command, excluded string }{
		{"chats", "/join", "/theme"},
		{"MODERATION", "/mute", "/join"},
		{" appearance ", "/theme", "/join"},
		{"network", "/relay", "/join"},
		{"general", "/help", "/join"},
	} {
		t.Run(tc.category, func(t *testing.T) {
			c, events := testClient(t)
			c.handleAction(UserAction{Type: "GET_HELP", Payload: tc.category})
			event := <-events
			if event.Type != "INFO" || !strings.HasPrefix(event.Content, strings.ToUpper(strings.TrimSpace(tc.category))+":\n") {
				t.Fatalf("unexpected category help: %+v", event)
			}
			if !strings.Contains(event.Content, tc.command) || strings.Contains(event.Content, tc.excluded) {
				t.Fatalf("category did not isolate its commands: %s", event.Content)
			}
		})
	}
	c, events := testClient(t)
	c.handleAction(UserAction{Type: "GET_HELP", Payload: "missing"})
	event := <-events
	if event.Type != "INFO" || !strings.Contains(event.Content, "Unknown help category") || !strings.Contains(event.Content, "chats") || strings.Contains(event.Content, "/join") {
		t.Fatalf("unknown category did not return a concise category hint: %+v", event)
	}
}
