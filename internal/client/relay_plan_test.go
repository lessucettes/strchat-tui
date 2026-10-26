package client

import (
	"fmt"
	"maps"
	"slices"
	"testing"

	"fiatjaf.com/nostr"
)

func randomChatSession(t *testing.T) chatSession {
	t.Helper()
	sk := nostr.Generate()
	pub := sk.Public().Hex()
	return chatSession{privKey: sk.Hex(), pubKey: pub, nick: pub[:4]}
}

func TestRelayPlanningRebuildsWhenAnchorsChange(t *testing.T) {
	c, _ := testClient(t)
	c.config.Views = []View{{Name: "lobby"}}
	c.config.ActiveViewName = "lobby"
	c.config.AnchorRelays = []string{"127.0.0.1:9001"}
	c.updateAllSubscriptions()
	if got := c.getRelayPoolForChat("lobby"); !slices.Equal(got, []string{"wss://127.0.0.1:9001"}) {
		t.Fatalf("pool = %v", got)
	}
	// Mutating the configuration directly is detected by the per-pass anchor
	// sync, so even non-manageAnchors paths cannot serve a stale plan.
	c.config.AnchorRelays = []string{"WSS://Relay.Example.COM:443/", "ws://127.0.0.1:9002", "http://invalid.example"}
	want := []string{"wss://relay.example.com:443", "ws://127.0.0.1:9002"}
	if got := c.getRelayPoolForChat("lobby"); !slices.Equal(got, want) {
		t.Fatalf("pool after anchor change = %v, want %v", got, want)
	}
	c.updateAllSubscriptions()
	if _, ok := c.relays["wss://relay.example.com:443"]; !ok {
		t.Fatalf("new anchor did not become a selected relay; selected=%v", slices.Collect(maps.Keys(c.relays)))
	}
	if _, ok := c.relays["wss://127.0.0.1:9001"]; ok {
		t.Fatal("removed anchor stayed selected")
	}
}

func TestRelayRefreshSkipsUnchangedSubscription(t *testing.T) {
	c, _ := testClient(t)
	url := "ws://127.0.0.1:9009"
	desired := map[string][]string{url: {"lobby"}}
	subscription := c.subscriptionForRelay(url, []string{"lobby"})
	worker := &managedRelay{url: url, chats: []string{"lobby"}, keys: subscription.keys,
		updates: make(chan relaySubscription, 1), cancel: func() {}}
	c.relays[url] = worker

	c.updateRelaySubscriptions(desired)
	if worker.keys == nil {
		t.Fatal("seeded identity snapshot was dropped")
	}
	select {
	case update := <-worker.updates:
		t.Fatalf("unchanged topology produced a subscription update: %+v", update)
	default:
	}
	before := fmt.Sprintf("%p", worker.keys)
	c.updateRelaySubscriptions(desired)
	if after := fmt.Sprintf("%p", worker.keys); after != before {
		t.Fatal("unchanged topology replaced the identity snapshot")
	}

	// The same chat set with a new per-chat identity is a real change even
	// though the names are unchanged; it must reach the worker (and therefore
	// AUTH/prune) immediately.
	rotated := randomChatSession(t)
	c.chatKeys["lobby"] = rotated
	c.updateRelaySubscriptions(desired)
	select {
	case update := <-worker.updates:
		if update.keys[rotated.pubKey] != rotated.privKey {
			t.Fatalf("rotated identity missing from subscription: %v", update.keys)
		}
		if !slices.Equal(update.chats, []string{"lobby"}) {
			t.Fatalf("chat set changed unexpectedly: %v", update.chats)
		}
	default:
		t.Fatal("identity rotation did not update the subscription")
	}
}

func TestRelayPlansArePrunedWithInactiveChats(t *testing.T) {
	c, _ := testClient(t)
	c.config.Views = []View{{Name: "group", IsGroup: true, Children: []string{"u4pruy", "9q8yyk"}}}
	c.config.ActiveViewName = "group"
	c.geoPlans["u4pruy"] = [][]string{{"ws://127.0.0.1:9100"}}
	c.geoPlans["9q8yyk"] = [][]string{{"ws://127.0.0.1:9101"}}
	c.invalidateRelayPlanning()
	c.config.Views = []View{{Name: "u4pruy"}}
	c.config.ActiveViewName = "u4pruy"
	c.updateAllSubscriptions()
	if len(c.relayPlans.plans) != 1 {
		t.Fatalf("cached plans = %d, want 1", len(c.relayPlans.plans))
	}
	if len(c.geoPlans) != 1 {
		t.Fatalf("geo plans = %d, want 1", len(c.geoPlans))
	}
	if _, ok := c.relayPlans.plans["9q8yyk"]; ok {
		t.Fatal("inactive chat kept a cached plan")
	}
}
