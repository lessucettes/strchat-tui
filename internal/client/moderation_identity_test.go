package client

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBlockRejectsAmbiguousNickname(t *testing.T) {
	c, events := testClient(t)
	c.config.path = filepath.Join(t.TempDir(), "config.json")
	for _, pk := range []string{strings.Repeat("a", 60) + "beef", strings.Repeat("b", 60) + "beef"} {
		c.userContext.Add(pk, userContext{nick: "alice", shortPubKey: "beef", chat: "lobby"})
	}
	c.blockUser("@alice#beef")
	if len(c.config.BlockedUsers) != 0 {
		t.Fatal("ambiguous nickname blocked an arbitrary participant")
	}
	if event := <-events; event.Type != "ERROR" {
		t.Fatalf("expected ambiguity error, got %s", event.Type)
	}
}

func TestBlockAcceptsFullKeyCompletion(t *testing.T) {
	c, _ := testClient(t)
	c.config.path = filepath.Join(t.TempDir(), "config.json")
	pk := strings.Repeat("a", 60) + "beef"
	c.userContext.Add(pk, userContext{nick: "alice", shortPubKey: "beef", chat: "lobby"})
	c.blockUser("@alice#" + pk)
	if len(c.config.BlockedUsers) != 1 || c.config.BlockedUsers[0].PubKey != pk {
		t.Fatal("full-key completion did not block the selected participant")
	}
}

func TestUnblockAcceptsFullKeyCompletion(t *testing.T) {
	c, _ := testClient(t)
	c.config.path = filepath.Join(t.TempDir(), "config.json")
	pk := strings.Repeat("a", 60) + "beef"
	c.config.BlockedUsers = []blockedUser{{PubKey: pk, Nick: "alice#beef"}}
	c.unblockUser("@alice#" + pk)
	if len(c.config.BlockedUsers) != 0 {
		t.Fatal("full-key completion did not unblock the selected participant")
	}
}

func TestUnblockRejectsAmbiguousNickname(t *testing.T) {
	c, events := testClient(t)
	c.config.path = filepath.Join(t.TempDir(), "config.json")
	c.config.BlockedUsers = []blockedUser{
		{PubKey: strings.Repeat("a", 64), Nick: "alice#beef"},
		{PubKey: strings.Repeat("b", 64), Nick: "alice#cafe"},
	}
	c.unblockUser("@ali")
	if len(c.config.BlockedUsers) != 2 {
		t.Fatal("ambiguous nickname unblocked an arbitrary participant")
	}
	if event := <-events; event.Type != "ERROR" {
		t.Fatalf("expected ambiguity error, got %s", event.Type)
	}
}
