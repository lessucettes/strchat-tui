package tui

import (
	"strings"
	"testing"
)

func TestFullKeyRecipientCompletesNickname(t *testing.T) {
	identifier := "alice#" + strings.Repeat("a", 64)
	for _, trailing := range []string{"", " ", " hello"} {
		nick, complete := extractNickPrefix("@" + identifier + trailing)
		if !complete || nick != identifier {
			t.Fatalf("full-key recipient not complete: nick=%q complete=%t", nick, complete)
		}
	}
	ui := newIdleTUI(t, 1)
	ui.completionEntries = []string{"@" + identifier + " "}
	if entries := ui.handleAutocomplete("@" + identifier + " hello"); len(entries) != 0 {
		t.Fatal("completion remained active after selecting a full-key recipient")
	}
}
