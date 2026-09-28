package client

import (
	"path/filepath"
	"strconv"
	"testing"
)

func TestStateUpdateIsAnImmutableSnapshot(t *testing.T) {
	c, events := testClient(t)
	c.config.Views = append(c.config.Views, View{Name: "group", IsGroup: true, Children: []string{"lobby", "other"}})
	c.sendStateUpdate()
	update := (<-events).Payload.(StateUpdate)
	c.config.Views[0].PoW = 12
	c.config.Views[1].Children[0] = "changed"
	if update.Views[0].PoW != 0 || update.Views[1].Children[0] != "lobby" {
		t.Fatal("UI snapshot shares mutable config storage")
	}
}

func TestViewSwitchReusesSessionIdentity(t *testing.T) {
	c, _ := testClient(t)
	c.config.path = filepath.Join(t.TempDir(), "config.json")
	c.setActiveView("lobby")
	key := c.chatKeys["lobby"].pubKey
	c.setActiveView("lobby")
	if c.chatKeys["lobby"].pubKey != key {
		t.Fatal("view switch unexpectedly rotated identity")
	}
}

func TestPoWDifficultyIsBounded(t *testing.T) {
	c, _ := testClient(t)
	c.config.path = filepath.Join(t.TempDir(), "config.json")
	c.setPoW("257")
	if c.config.Views[0].PoW != 0 {
		t.Fatal("accepted impossible work difficulty")
	}
	c.setPoW("-1")
	if c.config.Views[0].PoW != 0 {
		t.Fatal("accepted negative work difficulty")
	}
	// The largest accepted target must be reachable inside the publish deadline;
	// accepting a larger one would guarantee every send fails after mining.
	c.setPoW(strconv.Itoa(maxPoW))
	if c.config.Views[0].PoW != maxPoW {
		t.Fatalf("rejected the largest supported difficulty %d", maxPoW)
	}
	c.setPoW(strconv.Itoa(maxPoW + 1))
	if c.config.Views[0].PoW != maxPoW {
		t.Fatalf("accepted the unmineable difficulty %d", maxPoW+1)
	}
}

func TestMalformedBlockedKeyDoesNotPanic(t *testing.T) {
	c, _ := testClient(t)
	c.config.path = filepath.Join(t.TempDir(), "config.json")
	c.config.BlockedUsers = []blockedUser{{PubKey: "a"}}
	c.listBlockedUsers()
	c.unblockUser("1")
}
