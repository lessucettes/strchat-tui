package client

import (
	"encoding/json"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"fiatjaf.com/nostr"
	"github.com/coder/websocket"
)

func readPresenceCounts(t *testing.T, events <-chan DisplayEvent) map[string]int {
	t.Helper()
	select {
	case event := <-events:
		if event.Type != "PRESENCE_UPDATE" {
			t.Fatalf("expected participant counts, got %+v", event)
		}
		return event.Payload.(map[string]int)
	default:
		t.Fatal("participant counts were not published")
		return nil
	}
}

func TestPresenceReceiveDoesNotBecomeAMessage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, events := testClient(t)
		// Heartbeats must not inherit a chat's message PoW or text filter.
		c.config.Views[0].PoW = 16
		c.config.Filters = []filter{{Pattern: "hello", Enabled: true}}
		c.rebuildRegexCaches()
		c.wg.Go(func() { c.activity.run(c.ctx, c.eventsChan) })
		defer c.shutdown()
		event := &nostr.Event{Kind: 20001, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", "lobby"}}}
		if err := event.Sign(nostr.MustSecretKeyFromHex(transportTestKey)); err != nil {
			t.Fatal(err)
		}
		c.activity.heartbeat(event)
		c.activity.heartbeat(event)
		synctest.Wait()
		time.Sleep(activityInterval)
		synctest.Wait()
		counts := readPresenceCounts(t, events)
		if counts["lobby"] != 1 {
			t.Fatalf("heartbeat did not produce a participant count: %v", counts)
		}
		if c.seenCache.Contains(event.ID) || c.authorVerify.len() != 0 || len(events) != 0 || c.userContext.Len() != 0 {
			t.Fatal("presence touched message caches or nickname completion")
		}
		// Verified messages contribute even when their text is filtered. The
		// first author merges with the heartbeat; the second adds one participant.
		c.config.Views[0].PoW = 0
		c.processEvent(signedEvent(t, "filtered"), "relay")
		c.processEvent(signedChatEvent(t, fmt.Sprintf("%064x", 2), ephChatKind, "lobby", "filtered", nostr.Now()), "relay")
		synctest.Wait()
		time.Sleep(activityInterval)
		synctest.Wait()
		counts = readPresenceCounts(t, events)
		if counts["lobby"] != 2 || len(events) != 0 || c.userContext.Len() != 0 {
			t.Fatalf("filtered messages did not merge with heartbeat activity: %v", counts)
		}
	})
}

func TestPresenceSubscriptionIncludesGeoAndNamed(t *testing.T) {
	filters := chatFilters([]string{"lobby", "u33dc"}, nostr.Now())
	for _, tag := range []nostr.Tag{{"d", "lobby"}, {"g", "u33dc"}} {
		event := &nostr.Event{Kind: 20001, CreatedAt: nostr.Now(), Tags: nostr.Tags{tag}}
		if !filtersMatch(filters, event) {
			t.Fatalf("presence for %v is absent from subscription", tag)
		}
	}
}

func TestPresenceBurstBypassesMessageQueue(t *testing.T) {
	c, events := testClient(t)
	beat := &nostr.Event{Kind: 20001, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", "lobby"}}}
	if err := beat.Sign(nostr.MustSecretKeyFromHex(transportTestKey)); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal([]any{"EVENT", "chat", beat})
	if err != nil {
		t.Fatal(err)
	}
	// No activity consumer is running: its bounded intake must overflow without
	// holding up the socket reader or the ordinary message behind this burst.
	burst := make([][]byte, 256)
	for i := range burst {
		burst[i] = data
	}
	want := signedEvent(t, "after presence burst")
	data, err = json.Marshal([]any{"EVENT", "chat", want})
	if err != nil {
		t.Fatal(err)
	}
	peer := newRelayPingPeer(t, append(burst, data), true)
	incoming := make(chan relayEvent, 1)
	cancel, result, ticks := startRelayPingServe(t, c, peer, time.Second, incoming)
	defer stopRelayPingServe(t, cancel, result)
	select {
	case status := <-incoming:
		if status.event != nil || !status.connected {
			t.Fatal("missing initial relay status")
		}
	case <-time.After(transportWaitOp):
		t.Fatal("relay did not connect")
	}
	sendRelayPingTick(t, ticks, result)
	got := waitRelayEvents(t, incoming, 1, transportWaitOp)[0]
	if got.ID != want.ID {
		t.Fatalf("presence entered the message queue: kind %d, content %q", got.Kind, got.Content)
	}
	for range 256 {
		c.activity.observe("lobby", "existing participant", nostr.Now())
	}
	c.processEvent(got, peer.url)
	select {
	case event := <-events:
		if event.Type != "NEW_MESSAGE" || event.Content != want.Content {
			t.Fatalf("activity overload affected message presentation: %+v", event)
		}
	default:
		t.Fatal("full activity queues prevented message presentation")
	}
}

func TestActivityWorkerCoalescesWhileDisplayIsBlocked(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _ := testClient(t)
		events := make(chan DisplayEvent, 1)
		c.eventsChan = events
		events <- DisplayEvent{Type: "STATUS", Content: "occupied"}
		c.config.Filters = []filter{{Pattern: "hello", Enabled: true}}
		c.rebuildRegexCaches()
		c.wg.Go(func() { c.activity.run(c.ctx, c.eventsChan) })
		defer c.shutdown()
		// Exhaust this turn's signature budget with otherwise admissible events.
		bad := &nostr.Event{Kind: 20001, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", "lobby"}}}
		if err := bad.Sign(nostr.MustSecretKeyFromHex(transportTestKey)); err != nil {
			t.Fatal(err)
		}
		bad.Sig = [64]byte{}
		for range activityHeartbeatBudget {
			c.activity.heartbeat(bad)
		}
		synctest.Wait()
		beat := &nostr.Event{Kind: 20001, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", "lobby"}}}
		if err := beat.Sign(nostr.MustSecretKeyFromHex(fmt.Sprintf("%064x", 2))); err != nil {
			t.Fatal(err)
		}
		c.activity.heartbeat(beat)
		c.processEvent(signedEvent(t, "filtered"), "relay")
		synctest.Wait()
		time.Sleep(2 * activityInterval)
		synctest.Wait()
		if event := <-events; event.Type != "STATUS" {
			t.Fatalf("activity displaced a queued display event: %+v", event)
		}
		time.Sleep(activityInterval)
		synctest.Wait()
		counts := readPresenceCounts(t, events)
		if counts["lobby"] != 2 || len(events) != 0 {
			t.Fatalf("blocked display lost the latest coalesced counts: %v", counts)
		}
		// Config changes prune counts without messages or the main owner loop.
		oldPolicy := c.activity.policy.Load()
		c.config.BlockedUsers = []blockedUser{{PubKey: beat.PubKey.Hex()}}
		c.syncActivityPolicy()
		c.activity.heartbeats <- activityHeartbeat{policy: oldPolicy, event: beat}
		synctest.Wait()
		time.Sleep(activityInterval)
		synctest.Wait()
		if updated := readPresenceCounts(t, events); updated["lobby"] != 1 || counts["lobby"] != 2 {
			t.Fatalf("blocking failed to prune activity or mutated a snapshot: %v", updated)
		}
		c.config.Views = []View{{Name: "coffee"}}
		c.config.ActiveViewName = "coffee"
		c.syncActivityPolicy()
		c.activity.observe("lobby", bad.PubKey.Hex(), nostr.Now())
		synctest.Wait()
		time.Sleep(activityInterval)
		synctest.Wait()
		if updated := readPresenceCounts(t, events); len(updated) != 0 {
			t.Fatalf("inactive chat retained or regained participants: %v", updated)
		}
		c.processEvent(signedChatEvent(t, transportTestKey, ephChatKind, "coffee", "filtered", nostr.Now()), "relay")
		synctest.Wait()
		time.Sleep(activityInterval)
		synctest.Wait()
		if updated := readPresenceCounts(t, events); updated["coffee"] != 1 {
			t.Fatalf("new chat's activity was not admitted: %v", updated)
		}
		time.Sleep(presenceWindow + 10*time.Second)
		synctest.Wait()
		if updated := readPresenceCounts(t, events); len(updated) != 0 {
			t.Fatalf("activity did not expire independently: %v", updated)
		}
	})
}

func TestPresenceValidationAndActivityExpiry(t *testing.T) {
	c, _ := testClient(t)
	c.config.Views = []View{{Name: "mixed", IsGroup: true, Children: []string{"lobby", "u33dc"}}}
	c.config.ActiveViewName = "mixed"
	c.syncActivityPolicy()
	activity := newActivityState(c.activity.policy.Load())
	now := nostr.Now()
	heartbeat := func(tag nostr.Tag, at nostr.Timestamp, key string) *nostr.Event {
		t.Helper()
		event := &nostr.Event{Kind: 20001, CreatedAt: at, Tags: nostr.Tags{tag}}
		if err := event.Sign(nostr.MustSecretKeyFromHex(key)); err != nil {
			t.Fatal(err)
		}
		return event
	}
	for _, bad := range []struct {
		name string
		edit func(*nostr.Event)
	}{
		{"nonempty", func(e *nostr.Event) { e.Content = " " }},
		{"mixed tags", func(e *nostr.Event) { e.Tags = append(e.Tags, nostr.Tag{"g", "u33dc"}) }},
		{"duplicate tag", func(e *nostr.Event) { e.Tags = append(e.Tags, nostr.Tag{"d", "lobby"}) }},
		{"wrong channel type", func(e *nostr.Event) { e.Tags = nostr.Tags{{"d", "u33dc"}} }},
		{"inactive", func(e *nostr.Event) { e.Tags = nostr.Tags{{"d", "coffee"}} }},
		{"stale", func(e *nostr.Event) { e.CreatedAt = now - 301 }},
		{"future", func(e *nostr.Event) { e.CreatedAt = now + 601 }},
		{"forged", func(e *nostr.Event) {}},
		{"forged ID", func(e *nostr.Event) {}},
	} {
		t.Run(bad.name, func(t *testing.T) {
			event := heartbeat(nostr.Tag{"d", "lobby"}, now, transportTestKey)
			bad.edit(event)
			if err := event.Sign(nostr.MustSecretKeyFromHex(transportTestKey)); err != nil {
				t.Fatal(err)
			}
			if bad.name == "forged" {
				event.Sig = [64]byte{}
			}
			if bad.name == "forged ID" {
				event.ID = nostr.ID{}
			}
			activity.heartbeat(event, now)
			if activity.entries != 0 {
				t.Fatal("invalid heartbeat affected participant count")
			}
		})
	}
	key2 := fmt.Sprintf("%064x", 2)
	beat := heartbeat(nostr.Tag{"d", "lobby"}, now-100, transportTestKey)
	activity.remember("lobby", beat.PubKey.Hex(), now-200, now)
	activity.heartbeat(beat, now)
	activity.heartbeat(beat, now)
	activity.remember("lobby", beat.PubKey.Hex(), now-150, now)
	activity.heartbeat(heartbeat(nostr.Tag{"g", "u33dc"}, now-50, key2), now)
	activity.refreshCounts()
	if activity.counts["lobby"] != 1 || activity.counts["u33dc"] != 1 {
		t.Fatalf("messages and heartbeats did not merge by chat/key: %v", activity.counts)
	}
	activity.expire(now + 199)
	if activity.entries != 2 {
		t.Fatal("older replay shortened an active participant's expiry")
	}
	activity.expire(now + 200)
	activity.refreshCounts()
	if activity.counts["lobby"] != 0 || activity.counts["u33dc"] != 1 {
		t.Fatalf("participant expiry used receipt time instead of signed time: %v", activity.counts)
	}
	activity.heartbeat(heartbeat(nostr.Tag{"d", "lobby"}, now+30, transportTestKey), now)
	activity.expire(now + 300)
	if activity.entries != 0 {
		t.Fatal("future timestamp prolonged participant presence")
	}
	c.config.BlockedUsers = []blockedUser{{PubKey: beat.PubKey.Hex()}}
	c.syncActivityPolicy()
	activity.setPolicy(c.activity.policy.Load())
	blocked := heartbeat(nostr.Tag{"d", "lobby"}, now+1, transportTestKey)
	activity.heartbeat(blocked, now)
	if activity.entries != 0 {
		t.Fatal("blocked participant returned through a heartbeat")
	}
	c.config.BlockedUsers = nil
	c.syncActivityPolicy()
	activity.setPolicy(c.activity.policy.Load())
	activity.heartbeat(blocked, now)
	if activity.entries != 1 {
		t.Fatal("unblocking did not admit the same valid heartbeat")
	}
}

func TestPresenceAnnouncesOnlySelectedChatWithoutSwitchBursts(t *testing.T) {
	c, _ := testClient(t)
	c.config.Views = []View{{Name: "mixed", IsGroup: true, Children: []string{"lobby", "coffee", "u33dc"}, SelectedChat: "coffee"}}
	const preciseGeohash = "u33dc0123456"
	for precision := 6; precision <= 12; precision++ {
		c.config.Views[0].Children = append(c.config.Views[0].Children, preciseGeohash[:precision])
	}
	c.config.ActiveViewName = "mixed"
	c.ensureViewIdentities(c.getActiveView())
	shared := &managedRelay{connected: true, chats: c.config.Views[0].Children, heartbeat: make(chan relayHeartbeat, 1), publish: make(chan relayPublish, 1)}
	other := &managedRelay{connected: true, chats: []string{"lobby"}, heartbeat: make(chan relayHeartbeat, 1)}
	c.relays["shared"], c.relays["other"] = shared, other
	now := time.Now()
	c.syncPresenceTarget(now)
	c.sendPresence(now)
	if len(shared.heartbeat) != 0 {
		t.Fatal("destination selection announced immediately")
	}
	c.sendPresence(now.Add(5 * time.Second))
	if len(shared.heartbeat) != 1 {
		t.Fatal("selected chat did not announce presence")
	}
	first := <-shared.heartbeat
	if first.event.Kind != 20001 || first.event.Content != "" || len(first.event.Tags) != 1 ||
		len(first.event.Tags[0]) != 2 || first.event.Tags[0][0] != "d" || first.event.Tags[0][1] != "coffee" ||
		first.event.PubKey.Hex() != c.chatKeys["coffee"].pubKey || !first.event.VerifySignature() || len(other.heartbeat) != 0 {
		t.Fatalf("heartbeat leaked outside the selected chat or has incompatible shape: %+v", first.event)
	}
	if delay := c.presence.nextSend.Sub(now.Add(5 * time.Second)); delay < 40*time.Second || delay > 80*time.Second {
		t.Fatalf("unexpected heartbeat interval: %s", delay)
	}
	c.config.Views[0].SelectedChat = "u33dc"
	c.syncPresenceTarget(now.Add(6 * time.Second))
	if first.ctx.Err() == nil {
		t.Fatal("old destination's queued heartbeat remained live")
	}
	c.sendPresence(now.Add(11 * time.Second))
	if len(shared.heartbeat) != 0 {
		t.Fatal("switching destinations caused a heartbeat burst")
	}
	c.sendPresence(now.Add(46 * time.Second))
	if len(shared.heartbeat) != 1 {
		t.Fatal("geo destination did not announce presence")
	}
	geo := <-shared.heartbeat
	if geo.event.Tags[0][0] != "g" || geo.event.Tags[0][1] != "u33dc" || geo.event.PubKey.Hex() != c.chatKeys["u33dc"].pubKey || !geo.event.VerifySignature() {
		t.Fatalf("geo heartbeat has wrong channel or identity: %+v", geo.event)
	}
	for precision := 6; precision <= 12; precision++ {
		chat := preciseGeohash[:precision]
		c.config.Views[0].SelectedChat = chat
		switchedAt := now.Add(time.Duration(precision) * time.Minute)
		c.syncPresenceTarget(switchedAt)
		c.sendPresence(switchedAt.Add(5 * time.Second))
		if len(shared.heartbeat) != 1 {
			t.Fatalf("selected geohash %q did not announce presence", chat)
		}
		heartbeat := <-shared.heartbeat
		if heartbeat.event.Kind != 20001 || heartbeat.event.Content != "" || len(heartbeat.event.Tags) != 1 ||
			len(heartbeat.event.Tags[0]) != 2 || heartbeat.event.Tags[0][0] != "g" || heartbeat.event.Tags[0][1] != chat ||
			heartbeat.event.PubKey.Hex() != c.chatKeys[chat].pubKey || !heartbeat.event.VerifySignature() || len(other.heartbeat) != 0 {
			t.Fatalf("precise heartbeat changed channel, identity or relay scope: %+v", heartbeat.event)
		}
	}
}

func TestPresenceBoundsAndInactiveChatPruning(t *testing.T) {
	c, _ := testClient(t)
	view := View{Name: "mixed", IsGroup: true}
	for i := range presenceTotalCap/presenceChannelCap + 1 {
		view.Children = append(view.Children, fmt.Sprintf("room%d", i))
	}
	c.config.Views = []View{view}
	c.config.ActiveViewName = view.Name
	c.syncActivityPolicy()
	activity := newActivityState(c.activity.policy.Load())
	now := nostr.Now()
	for _, chat := range view.Children {
		for i := range presenceChannelCap + 1 {
			activity.remember(chat, fmt.Sprintf("participant-%d", i), now-1, now)
		}
	}
	if activity.entries != presenceTotalCap {
		t.Fatalf("presence exceeded or underfilled total bound: %d", activity.entries)
	}
	for _, members := range activity.members {
		if len(members) > presenceChannelCap {
			t.Fatal("presence exceeded its per-channel bound")
		}
	}
	activity.refreshCounts()
	oldSnapshot := activity.counts
	activity.remember("room0", "participant-0", now, now)
	if activity.members["room0"]["participant-0"] != now {
		t.Fatal("a full table prevented renewal of a known participant")
	}
	c.config.Views[0].Children = []string{"room0"}
	c.syncActivityPolicy()
	activity.setPolicy(c.activity.policy.Load())
	activity.refreshCounts()
	if activity.entries != presenceChannelCap || len(activity.counts) != 1 || len(oldSnapshot) != presenceTotalCap/presenceChannelCap {
		t.Fatal("inactive chats were retained or a published snapshot was mutated")
	}
}

func TestPresenceHeartbeatDoesNotWaitForAcknowledgement(t *testing.T) {
	server := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, _ int) {
		for {
			frame, ok := relayNextFrame(ts, conn)
			if !ok {
				return
			}
			var label string
			if len(frame) < 2 || json.Unmarshal(frame[0], &label) != nil {
				continue
			}
			switch label {
			case "REQ":
				relayTestNoteREQ(ts, frame)
			case "EVENT":
				var event nostr.Event
				if json.Unmarshal(frame[1], &event) != nil {
					return
				}
				ts.noteEvent(event)
				if event.Kind != 20001 && !relayWriteJSON(ts, conn, relayOKFrame(event.ID, true, "")) {
					return
				}
			}
		}
	})
	h := startRelayHarness(t, relayTestConfig(t, server.url), nil)
	server.waitREQs(1, transportWaitSlow)
	var heartbeat nostr.Event
	select {
	case heartbeat = <-server.eventCh:
	case <-time.After(transportWaitSlow):
		t.Fatal("Run did not publish a heartbeat")
	}
	if heartbeat.Kind != 20001 || heartbeat.Content != "" || len(heartbeat.Tags) != 1 || heartbeat.Tags[0][0] != "d" || heartbeat.Tags[0][1] != "lobby" || !heartbeat.VerifySignature() {
		t.Fatalf("incompatible heartbeat on the wire: %+v", heartbeat)
	}
	h.send(UserAction{Type: "SEND_MESSAGE", Payload: "after heartbeat"})
	var message nostr.Event
	select {
	case message = <-server.eventCh:
	case <-time.After(transportWaitOp):
		t.Fatal("missing heartbeat ACK blocked a user message")
	}
	if message.Kind != ephChatKind || message.Content != "after heartbeat" || message.PubKey != heartbeat.PubKey {
		t.Fatalf("message and heartbeat do not share the chat identity: %+v", message)
	}
	h.waitForEvent("own message", func(e DisplayEvent) bool { return e.Type == "NEW_MESSAGE" && e.Content == "after heartbeat" })
	if server.reqs.Load() != 1 {
		t.Fatal("heartbeat caused an unnecessary resubscription")
	}
}
