package client

import (
	"slices"
	"testing"
	"time"
)

func TestStableRelayResetsRetryHistoryBeforeViewSwitch(t *testing.T) {
	c, _ := testClient(t)
	url := "ws://127.0.0.1:12345"
	c.config.AnchorRelays = []string{url}
	c.config.Views = nil
	c.relayRetries.Add(url, relayRetry{delay: c.relayTiming.maximum})
	c.relays[url] = &managedRelay{url: url, connected: true,
		connectedAt: time.Now().Add(-2 * c.relayTiming.stable), cancel: func() {}}
	c.updateAllSubscriptions()
	if c.relayRetries.Contains(url) {
		t.Fatal("stable connection retained old failure penalty when leaving its view")
	}
}

func TestPrimaryRecoveryKeepsReserveNeededByAnotherChat(t *testing.T) {
	c, _ := testClient(t)
	chat, other := "u4pruy", "9q8yyk"
	c.config.Views = []View{{Name: "group", IsGroup: true, Children: []string{chat, other}}}
	c.config.ActiveViewName = "group"
	primary, reserve := "ws://127.0.0.1:12000", "ws://127.0.0.1:12001"
	common := []string{"ws://127.0.0.1:12002", "ws://127.0.0.1:12003", "ws://127.0.0.1:12004", "ws://127.0.0.1:12005"}
	c.geoPlans[chat] = [][]string{append(append([]string{primary}, common...), reserve)}
	c.geoPlans[other] = [][]string{append([]string{reserve}, common...)}
	seedRelaySelection(c, []string{chat, other})
	shared := c.relays[reserve]
	shared.connected = true
	shared.connectedAt = time.Now()
	c.relayRetries.Add(primary, relayRetry{until: time.Now().Add(time.Minute), delay: time.Minute})
	c.updateAllSubscriptions()
	if c.relays[reserve] != shared || !slices.Equal(shared.chats, []string{other, chat}) {
		t.Fatalf("shared reserve was not reused for both chats: %v", shared.chats)
	}
	c.relayRetries.Add(primary, relayRetry{delay: time.Minute})
	c.relays[primary] = &managedRelay{url: primary, connected: true,
		connectedAt: time.Now().Add(-2 * time.Minute), chats: []string{chat},
		keys: c.subscriptionForRelay(primary, []string{chat}).keys, cancel: func() {}}
	c.updateAllSubscriptions()
	if c.relays[reserve] != shared || !slices.Equal(shared.chats, []string{other}) {
		t.Fatalf("primary recovery retired another chat's working relay: %v", shared.chats)
	}
}
