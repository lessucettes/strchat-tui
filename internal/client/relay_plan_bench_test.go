package client

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

// Persistent refresh fixtures for the relay selection/planning hot path.
//
// Shape mirrors TestRelayGroupSafetyCeilingLoopback: 20 geographic chats, both
// bundled catalog families (five nearest each), and enough explicit anchor
// relays to reach the 256-socket safety ceiling. There is no network activity:
// workers are inert owner-side handles and the benchmark only exercises the
// owner goroutine work that used to run once per relay status event.

const relayPlanBenchAnchorCount = maxActiveRelays - 20*2*defaultRelayCount

func newRelayPlanBenchClient(b *testing.B) (*client, []string, chan DisplayEvent) {
	b.Helper()
	chats := make([]string, 0, 20)
	plans := make(map[string][][]string, 20)
	for _, cell := range "0123456789bcdefghjkm" {
		chat := string(cell) + "00000"
		chats = append(chats, chat)
		lists := make([][]string, 2)
		for family := range lists {
			for rank := range defaultRelayCount {
				lists[family] = append(lists[family], fmt.Sprintf("ws://127.0.0.1:7777/geo/%s/%d/%d", chat, family, rank))
			}
		}
		plans[chat] = lists
	}
	cfg := &config{
		Views:          []View{{Name: "world", IsGroup: true, Children: chats}},
		ActiveViewName: "world",
	}
	for i := range relayPlanBenchAnchorCount {
		cfg.AnchorRelays = append(cfg.AnchorRelays, fmt.Sprintf("ws://127.0.0.1:7777/anchor/%d", i))
	}
	events := make(chan DisplayEvent, 4096)
	c := newClient(cfg, make(chan UserAction, 1), events)
	c.geoPlans = plans
	return c, chats, events
}

// seedRelayPlanBenchWorkers installs inert handles whose chats and identity
// snapshots already match the current selection, so a refresh must recognize
// the stable topology instead of replacing subscriptions.
func seedRelayPlanBenchWorkers(b *testing.B, c *client, chats []string) {
	b.Helper()
	desired, _ := c.selectRelaySubscriptions(chats, time.Now())
	for url, relayChats := range desired {
		relayChats = slices.Clone(relayChats)
		slices.Sort(relayChats)
		relayChats = slices.Compact(relayChats)
		c.relays[url] = &managedRelay{
			url: url, chats: relayChats, keys: c.subscriptionForRelay(url, relayChats).keys,
			connected: true, connectedAt: time.Now().Add(-2 * time.Minute),
			updates: make(chan relaySubscription, 1), cancel: func() {},
		}
	}
	if len(c.relays) != maxActiveRelays {
		b.Fatalf("fixture selected %d sockets, want %d", len(c.relays), maxActiveRelays)
	}
}

func drainRelayPlanBenchEvents(events chan DisplayEvent) {
	for {
		select {
		case <-events:
		default:
			return
		}
	}
}

// BenchmarkRelayRefreshStableTopology is the dominant owner-loop path: one
// relay status (connect, latency, retry or post-AUTH report) arrives while the
// desired relay/chat/identity topology is already established.
func BenchmarkRelayRefreshStableTopology(b *testing.B) {
	c, chats, events := newRelayPlanBenchClient(b)
	seedRelayPlanBenchWorkers(b, c, chats)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		c.updateAllSubscriptions()
		drainRelayPlanBenchEvents(events)
	}
}
