package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"fiatjaf.com/nostr"
)

func TestClearForgetsParticipantsButPreservesSessionAndReplayProtection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, events := testClient(t)
		c.config.path = filepath.Join(t.TempDir(), "config.json")
		c.config.AnchorRelays = []string{"ws://127.0.0.1:1"}
		c.config.Filters = []filter{{Pattern: "message", Enabled: true}}
		c.config.Mutes = []filter{{Pattern: "spam", Enabled: true}}
		c.config.BlockedUsers = []blockedUser{{PubKey: "blocked", Nick: "blocked-name"}}
		c.config.Views = append(c.config.Views, View{Name: "mixed", IsGroup: true, Children: []string{"lobby", "coffee"}, SelectedChat: "coffee"})
		c.config.ActiveViewName = "mixed"
		c.ensureViewIdentities(c.getActiveView())
		if err := c.config.save(); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(c.config.path)
		if err != nil {
			t.Fatal(err)
		}
		keyCount := len(c.chatKeys)
		identity := c.chatKeys["lobby"]
		worker := &managedRelay{url: "ws://127.0.0.1:1"}
		c.relays[worker.url] = worker
		old := signedEvent(t, "old message")
		c.syncActivityPolicy()
		c.processEvent(old, worker.url)
		c.syncPresenceTarget(time.Now())
		nextHeartbeat, heartbeatContext := c.presence.nextSend, c.presence.ctx
		for len(events) > 0 {
			<-events
		}
		c.wg.Go(func() { c.activity.run(c.ctx, c.eventsChan) })
		defer c.shutdown()
		synctest.Wait()
		time.Sleep(activityInterval)
		synctest.Wait()
		counts := readPresenceCounts(t, events)
		if c.userContext.Len() == 0 || c.authorVerify.len() == 0 || counts["lobby"] != 1 {
			t.Fatal("fixture did not populate participant and verification caches")
		}
		c.outgoing <- publishJob{chat: "lobby"}
		c.retiredKeys.Add("retired-public-key", true)

		c.handleAction(UserAction{Type: "CLEAR"})
		select {
		case event := <-events:
			if event.Type != "CLEARED" {
				t.Fatalf("clear emitted %s, want CLEARED", event.Type)
			}
		default:
			t.Fatal("clear did not acknowledge its ordered boundary")
		}
		if c.userContext.Len() != 0 || c.authorVerify.len() != 0 {
			t.Fatal("clear retained participant or verification data")
		}
		if c.presence.nextSend != nextHeartbeat || c.presence.ctx != heartbeatContext || heartbeatContext.Err() != nil {
			t.Fatal("clear changed participant counts, liveness or the heartbeat schedule")
		}
		after, err := os.ReadFile(c.config.path)
		if err != nil {
			t.Fatal(err)
		}
		current, err := json.Marshal(c.config)
		if err != nil {
			t.Fatal(err)
		}
		var diskConfig config
		if err := json.Unmarshal(before, &diskConfig); err != nil {
			t.Fatal(err)
		}
		stored, _ := json.Marshal(diskConfig)
		if string(before) != string(after) || string(current) != string(stored) {
			t.Fatal("clear changed configuration or wrote the config file")
		}
		if c.chatKeys["lobby"].privKey != identity.privKey || len(c.chatKeys) != keyCount || c.relays[worker.url] != worker || len(c.outgoing) != 1 || !c.retiredKeys.Contains("retired-public-key") {
			t.Fatal("clear changed identity, transport, pending sends or own-echo tracking")
		}
		c.processEvent(old, worker.url)
		if len(events) != 0 || c.userContext.Len() != 0 || !c.seenCache.Contains(old.ID) {
			t.Fatal("a previously processed event returned after clear")
		}
		c.processEvent(signedEvent(t, "new message"), worker.url)
		if c.userContext.Len() != 1 || len(events) != 1 || (<-events).Type != "NEW_MESSAGE" {
			t.Fatal("new traffic did not resume after clear")
		}
		beat := &nostr.Event{Kind: 20001, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", "lobby"}}}
		if err := beat.Sign(nostr.MustSecretKeyFromHex("0000000000000000000000000000000000000000000000000000000000000002")); err != nil {
			t.Fatal(err)
		}
		c.activity.heartbeat(beat)
		synctest.Wait()
		time.Sleep(activityInterval)
		synctest.Wait()
		if updated := readPresenceCounts(t, events); updated["lobby"] != 2 || counts["lobby"] != 1 {
			t.Fatalf("clear discarded activity or mutated its published snapshot: %v", updated)
		}
	})
}
