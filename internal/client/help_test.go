package client

import (
	"strings"
	"testing"
)

func TestHelpListsCommandsWithoutPaneNavigation(t *testing.T) {
	c, events := testClient(t)
	c.getHelp()
	event := <-events
	if event.Type != "INFO" || !strings.HasPrefix(event.Content, "COMMANDS:\n") {
		t.Fatalf("unexpected command help: %+v", event)
	}
	for _, command := range []string{"/join", "/set", "/quit"} {
		if !strings.Contains(event.Content, command) {
			t.Errorf("command help is missing %s", command)
		}
	}
	if strings.Contains(event.Content, "* Groups:") || strings.Contains(event.Content, "Alt+N") {
		t.Fatal("command help includes unrelated pane navigation instructions")
	}
}
