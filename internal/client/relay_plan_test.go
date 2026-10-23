package client

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mmcloughlin/geohash"
	"github.com/nbd-wtf/go-nostr"
)

// The functions below are a literal copy of the pre-planning implementation.
// They are the oracle for randomized equivalence: a fresh reference client is
// built from the mutated production state on every step, so cached plans are
// compared against uncached whole-pool reconstruction.

func legacyGetRelayPoolForChat(c *client, chat string) []string {
	urls := slices.Clone(c.config.AnchorRelays)
	if geohash.Validate(chat) == nil {
		lists, ok := c.geoPlans[chat]
		if !ok {
			lists, _ = rankedGeoRelays(chat, 2*defaultRelayCount)
			c.geoPlans[chat] = lists
		}
		urls = append(urls, primaryGeoRelays(lists)...)
	}
	if len(urls) == 0 {
		urls = defaultEphChatRelays
	}
	seen := make(map[string]bool)
	var result []string
	for _, raw := range urls {
		url, err := normalizeRelayURL(raw)
		if err == nil && !seen[url] {
			seen[url] = true
			result = append(result, url)
		}
	}
	return result
}

func legacyGeoReserveForChat(c *client, chat string, now time.Time) ([]string, bool) {
	lists, ok := c.geoPlans[chat]
	if !ok {
		var err error
		lists, err = rankedGeoRelays(chat, 2*defaultRelayCount)
		if err != nil {
			return nil, true
		}
		c.geoPlans[chat] = lists
	}
	var reserves []string
	selected := make(map[string]bool)
	for _, url := range legacyGetRelayPoolForChat(c, chat) {
		selected[url] = true
	}
	exhausted := false
	for _, list := range lists {
		primaryCount := min(defaultRelayCount, len(list))
		missing := 0
		for _, url := range list[:primaryCount] {
			if !c.relayCovers(url, now) {
				missing++
			}
		}
		for _, connected := range []bool{true, false} {
			for _, url := range list[primaryCount:] {
				if missing == 0 {
					break
				}
				if selected[url] {
					continue
				}
				worker := c.relays[url]
				isConnected := worker != nil && worker.connected
				if isConnected == connected && c.relayEligible(url, now) {
					reserves = append(reserves, url)
					selected[url] = true
					missing--
				}
			}
		}
		exhausted = exhausted || missing > 0
	}
	return reserves, exhausted
}

func legacySelectRelaySubscriptions(c *client, chats []string, now time.Time) (map[string][]string, string) {
	c.nextRelayRetry = time.Time{}
	for url, worker := range c.relays {
		if worker.connected && !worker.connectedAt.Add(c.relayTiming.stable).After(now) {
			c.relayRetries.Remove(url)
		}
	}
	for chat := range c.geoPlans {
		if !slices.Contains(chats, chat) {
			delete(c.geoPlans, chat)
		}
	}
	primary := make([][]string, len(chats))
	anchors := make([][]string, len(chats))
	reserve := make([][]string, len(chats))
	exhausted, usingReserve, unavailable := false, false, false
	for i, chat := range chats {
		pool := legacyGetRelayPoolForChat(c, chat)
		isGeo := geohash.Validate(chat) == nil
		var rendezvous []string
		if isGeo {
			rendezvous = primaryGeoRelays(c.geoPlans[chat])
		}
		for _, url := range pool {
			if c.relayEligible(url, now) {
				if isGeo && !slices.Contains(rendezvous, url) {
					anchors[i] = append(anchors[i], url)
				} else {
					primary[i] = append(primary[i], url)
				}
			}
		}
		if isGeo {
			var missing bool
			reserve[i], missing = legacyGeoReserveForChat(c, chat, now)
			exhausted = exhausted || missing
			usingReserve = usingReserve || len(reserve[i]) > 0
		}
		unavailable = unavailable || len(primary[i])+len(anchors[i])+len(reserve[i]) == 0
	}
	desired := make(map[string][]string)
	limited := false
	for _, pools := range [][][]string{primary, anchors, reserve} {
		longest := 0
		for _, pool := range pools {
			longest = max(longest, len(pool))
		}
		for rank := range longest {
			for i, chat := range chats {
				if rank >= len(pools[i]) {
					continue
				}
				url := pools[i][rank]
				if _, exists := desired[url]; !exists && len(desired) >= maxActiveRelays {
					limited = true
					continue
				}
				desired[url] = append(desired[url], chat)
			}
		}
	}
	if limited {
		return desired, fmt.Sprintf("Warning: the %d-relay safety limit reduces chat coverage. Use fewer explicit relays, smaller groups, or shared relays.", maxActiveRelays)
	}
	if exhausted {
		return desired, "Warning: geographic relay coverage is incomplete; reserve exhausted or cooling down. Primary retries continue."
	}
	if unavailable {
		return desired, "Warning: no eligible relay for an active chat; waiting for retry cooldown."
	}
	if usingReserve {
		return desired, "Trying geographic reserve relays; primary recovery continues."
	}
	return desired, ""
}

func legacySubscriptionForRelay(c *client, url string, chats []string) relaySubscription {
	update := relaySubscription{chats: chats, since: nostr.Now(), keys: make(map[string]string)}
	for _, chat := range chats {
		if session, ok := c.chatKeys[chat]; ok {
			update.keys[session.pubKey] = session.privKey
		}
	}
	for _, chat := range chats {
		if _, isGeo := c.geoPlans[chat]; isGeo && !slices.Contains(legacyGetRelayPoolForChat(c, chat), url) {
			update.since -= 30
			break
		}
	}
	return update
}

func relayPlanReference(t *testing.T, source *client) *client {
	t.Helper()
	ref := newClient(&config{AnchorRelays: slices.Clone(source.config.AnchorRelays)}, nil, make(chan DisplayEvent, 1))
	t.Cleanup(ref.cancel)
	ref.chatKeys = maps.Clone(source.chatKeys)
	ref.geoPlans = make(map[string][][]string, len(source.geoPlans))
	for chat, lists := range source.geoPlans {
		ref.geoPlans[chat] = lists
	}
	ref.relayTiming = source.relayTiming
	for _, url := range source.relayRetries.Keys() {
		if retry, ok := source.relayRetries.Peek(url); ok {
			ref.relayRetries.Add(url, retry)
		}
	}
	for url, worker := range source.relays {
		ref.relays[url] = &managedRelay{url: url, connected: worker.connected, connectedAt: worker.connectedAt}
	}
	return ref
}

func normalizedRelayDesired(desired map[string][]string) map[string][]string {
	out := make(map[string][]string, len(desired))
	for url, chats := range desired {
		sorted := slices.Clone(chats)
		slices.Sort(sorted)
		out[url] = slices.Compact(sorted)
	}
	return out
}

func subscriptionsOverlap(sub relaySubscription) bool {
	return nostr.Now()-sub.since > 15
}

func randomChatSession(t *testing.T) chatSession {
	t.Helper()
	priv := nostr.GeneratePrivateKey()
	pub, err := nostr.GetPublicKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return chatSession{privKey: priv, pubKey: pub, nick: pub[:4]}
}

// TestRelayPlanningMatchesLegacySelectionRandomized compares cached plans with
// full pool reconstruction after randomized anchor, chat, identity, catalog and
// cooldown mutations, including the subscription snapshot and retry wake time.
func TestRelayPlanningMatchesLegacySelectionRandomized(t *testing.T) {
	geoChats := []string{"u4pruy", "9q8yyk", "xn774c"}
	namedChats := []string{"lobby", "devchat", "coffee"}
	allChats := append(slices.Clone(geoChats), namedChats...)
	candidates := []string{
		"ws://127.0.0.1:9000", "ws://127.0.0.1:9001", "ws://127.0.0.1:9002",
		"ws://127.0.0.1:9003", "ws://127.0.0.1:9004", "ws://127.0.0.1:9005",
		"127.0.0.1:9006", "WSS://Relay.Example.COM:443/", "  ws://127.0.0.1:9007  ",
	}
	anchorForms := []string{
		"ws://127.0.0.1:9000", "127.0.0.1:9001", "WSS://Anchor.Example.COM:443/",
		"  ws://127.0.0.1:9002  ", "http://invalid.example", "ws://127.0.0.1:9000",
	}
	randomLists := func(rng *rand.Rand) [][]string {
		lists := make([][]string, 1+rng.IntN(2))
		for family := range lists {
			for range defaultRelayCount + rng.IntN(4) {
				lists[family] = append(lists[family], candidates[rng.IntN(len(candidates))])
			}
		}
		return lists
	}

	for seed := uint64(1); seed <= 4; seed++ {
		rng := rand.New(rand.NewPCG(seed, 0x5eed))
		c, _ := testClient(t)
		c.config.AnchorRelays = []string{anchorForms[0], anchorForms[1]}
		for _, chat := range allChats {
			c.chatKeys[chat] = randomChatSession(t)
		}
		for _, chat := range geoChats {
			c.geoPlans[chat] = randomLists(rng)
		}
		c.invalidateRelayPlanning()
		var steps, notices, reserves, multiChat int

		for step := range 50 {
			// Anchors: append, remove, duplicate or replace with an odd form.
			switch rng.IntN(4) {
			case 0:
				c.config.AnchorRelays = append(c.config.AnchorRelays, anchorForms[rng.IntN(len(anchorForms))])
			case 1:
				if len(c.config.AnchorRelays) > 0 {
					i := rng.IntN(len(c.config.AnchorRelays))
					c.config.AnchorRelays = slices.Delete(c.config.AnchorRelays, i, i+1)
				}
			case 2:
				if len(c.config.AnchorRelays) > 0 {
					c.config.AnchorRelays[rng.IntN(len(c.config.AnchorRelays))] = anchorForms[rng.IntN(len(anchorForms))]
				}
			case 3:
				c.config.AnchorRelays = slices.Clone(c.config.AnchorRelays)
			}

			// Identities: rotate, drop or add a chat session.
			switch rng.IntN(3) {
			case 0:
				chat := allChats[rng.IntN(len(allChats))]
				c.chatKeys[chat] = randomChatSession(t)
			case 1:
				delete(c.chatKeys, allChats[rng.IntN(len(allChats))])
			case 2:
				chat := allChats[rng.IntN(len(allChats))]
				if _, ok := c.chatKeys[chat]; !ok {
					c.chatKeys[chat] = randomChatSession(t)
				}
			}

			// Catalog plans are static in production; replacing a stub models a
			// process restart and requires an explicit plan invalidation.
			if rng.IntN(3) == 0 {
				c.geoPlans[geoChats[rng.IntN(len(geoChats))]] = randomLists(rng)
				c.invalidateRelayPlanning()
			}

			// Cooldowns and stable connection state.
			for range 1 + rng.IntN(3) {
				url := candidates[rng.IntN(len(candidates))]
				normalized, err := normalizeRelayURL(url)
				if err != nil {
					continue
				}
				switch rng.IntN(3) {
				case 0:
					c.relayRetries.Add(normalized, relayRetry{
						until: time.Now().Add(time.Duration(rng.IntN(120)) * time.Second),
						delay: time.Duration(rng.IntN(30)+1) * time.Second,
					})
				case 1:
					c.relayRetries.Remove(normalized)
				case 2:
					c.relays[normalized] = &managedRelay{
						url: normalized, connected: rng.IntN(2) == 0,
						connectedAt: time.Now().Add(-time.Duration(rng.IntN(180)) * time.Second),
					}
				}
			}

			chats := make([]string, 0, len(allChats))
			for _, chat := range allChats {
				if rng.IntN(3) > 0 {
					chats = append(chats, chat)
				}
			}
			if len(chats) == 0 {
				chats = append(chats, allChats[rng.IntN(len(allChats))])
			}
			slices.Sort(chats)

			now := time.Now()
			ref := relayPlanReference(t, c)
			wantDesired, wantNotice := legacySelectRelaySubscriptions(ref, chats, now)
			gotDesired, gotNotice := c.selectRelaySubscriptions(chats, now)

			if step%10 == 0 && !c.nextRelayRetry.Equal(ref.nextRelayRetry) {
				t.Fatalf("seed %d step %d: retry wake = %v, want %v", seed, step, c.nextRelayRetry, ref.nextRelayRetry)
			}
			want, got := normalizedRelayDesired(wantDesired), normalizedRelayDesired(gotDesired)
			if len(got) != len(want) {
				t.Fatalf("seed %d step %d: %d desired relays, want %d\n got=%v\nwant=%v", seed, step, len(got), len(want), got, want)
			}
			for url, wantChats := range want {
				if !slices.Equal(got[url], wantChats) {
					t.Fatalf("seed %d step %d: relay %s chats = %v, want %v", seed, step, url, got[url], wantChats)
				}
			}
			if gotNotice != wantNotice {
				t.Fatalf("seed %d step %d: notice = %q, want %q", seed, step, gotNotice, wantNotice)
			}

			if gotNotice != "" {
				notices++
				if strings.Contains(gotNotice, "reserve") || strings.Contains(gotNotice, "coverage") {
					reserves++
				}
			}
			for _, relayChats := range gotDesired {
				if len(relayChats) > 1 {
					multiChat++
					break
				}
			}
			steps++

			// Compare the exact subscription snapshot for a bounded sample.
			urls := slices.Collect(maps.Keys(gotDesired))
			slices.Sort(urls)
			for i := 0; i < min(6, len(urls)); i++ {
				url := urls[rng.IntN(len(urls))]
				chatsForRelay := slices.Clone(gotDesired[url])
				slices.Sort(chatsForRelay)
				chatsForRelay = slices.Compact(chatsForRelay)
				gotSub := c.subscriptionForRelay(url, chatsForRelay)
				wantSub := legacySubscriptionForRelay(ref, url, chatsForRelay)
				if !slices.Equal(gotSub.chats, wantSub.chats) || !maps.Equal(gotSub.keys, wantSub.keys) {
					t.Fatalf("seed %d step %d: subscription for %s = %+v, want %+v", seed, step, url, gotSub, wantSub)
				}
				if subscriptionsOverlap(gotSub) != subscriptionsOverlap(wantSub) {
					t.Fatalf("seed %d step %d: subscription for %s overlap = %t, want %t", seed, step, url, subscriptionsOverlap(gotSub), subscriptionsOverlap(wantSub))
				}
			}
		}
		if steps != 50 || notices == 0 || reserves == 0 || multiChat == 0 {
			t.Fatalf("seed %d: weak scenario coverage steps=%d notices=%d reserveNotices=%d multiChat=%d",
				seed, steps, notices, reserves, multiChat)
		}
	}
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
