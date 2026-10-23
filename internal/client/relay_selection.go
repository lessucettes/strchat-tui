package client

import (
	"fmt"
	"slices"
	"time"

	"github.com/mmcloughlin/geohash"
	"github.com/nbd-wtf/go-nostr"
)

// Retry history belongs to the client loop, not to a particular socket/view.
// The bounded cache survives view switches but is never written to disk.
type relayRetry struct {
	until time.Time
	delay time.Duration
	since nostr.Timestamp
	chats []string
}

type relayTiming struct {
	minimum, maximum, stable, dialGap time.Duration
}

var defaultRelayTiming = relayTiming{
	minimum: 500 * time.Millisecond, maximum: 5 * time.Minute,
	stable: time.Minute, dialGap: 100 * time.Millisecond,
}

func (c *client) wakeRelaysAt(at time.Time) {
	if c.nextRelayRetry.IsZero() || at.Before(c.nextRelayRetry) {
		c.nextRelayRetry = at
	}
}

func (c *client) relayEligible(url string, now time.Time) bool {
	retry, _ := c.relayRetries.Peek(url)
	if retry.until.After(now) {
		c.wakeRelaysAt(retry.until)
		return false
	}
	if worker := c.relays[url]; retry.delay > 0 && worker != nil && worker.connected {
		stableAt := worker.connectedAt.Add(c.relayTiming.stable)
		if stableAt.After(now) {
			c.wakeRelaysAt(stableAt)
		}
	}
	return true
}

func (c *client) relayCovers(url string, now time.Time) bool {
	retry, _ := c.relayRetries.Peek(url)
	if retry.until.After(now) {
		return false
	}
	if retry.delay == 0 {
		return true // Untried primary: let its first attempt finish before probing reserves.
	}
	worker := c.relays[url]
	return worker != nil && worker.connected && !worker.connectedAt.Add(c.relayTiming.stable).After(now)
}

// A newly used reserve, including a shared socket gaining another chat, needs
// the same bounded recovery overlap as a reconnect. Ordinary joins stay live-first.
func (c *client) subscriptionForRelay(url string, chats []string) relaySubscription {
	update := relaySubscription{chats: chats, since: nostr.Now(), keys: make(map[string]string)}
	for _, chat := range chats {
		if session, ok := c.chatKeys[chat]; ok {
			update.keys[session.pubKey] = session.privKey
		}
	}
	for _, chat := range chats {
		if _, isGeo := c.geoPlans[chat]; isGeo && !slices.Contains(c.getRelayPoolForChat(chat), url) {
			update.since -= 30
			break
		}
	}
	return update
}

// Preserve each catalog's nearest five independently. Only failed primaries
// open a deficit; a healthy reserve never cancels primary recovery attempts.
func (c *client) geoReserveForChat(chat string, now time.Time) ([]string, bool) {
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
	for _, url := range c.getRelayPoolForChat(chat) {
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
		// Keep established replacements before trying other reserve addresses.
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

func (c *client) selectRelaySubscriptions(chats []string, now time.Time) (map[string][]string, string) {
	c.nextRelayRetry = time.Time{}
	// Also do this before retiring a view's sockets: their successful stable
	// lifetime must clear old failure penalties even if no later error occurs.
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
		pool := c.getRelayPoolForChat(chat)
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
			reserve[i], missing = c.geoReserveForChat(chat, now)
			exhausted = exhausted || missing
			usingReserve = usingReserve || len(reserve[i]) > 0
		}
		unavailable = unavailable || len(primary[i])+len(anchors[i])+len(reserve[i]) == 0
	}
	desired := make(map[string][]string)
	limited := false
	// Preserve geo rendezvous ahead of extra anchors, and all primary pools
	// ahead of reserves. Each tier is rank-major across the active chats.
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

func (c *client) updateAllSubscriptions() {
	var chats []string
	var viewName string
	if view := c.getActiveView(); view != nil {
		c.ensureViewIdentities(view)
		viewName = view.Name
		if view.IsGroup {
			chats = slices.Clone(view.Children)
		} else {
			chats = []string{view.Name}
		}
	}
	slices.Sort(chats)
	chats = slices.Compact(chats)
	desired, notice := c.selectRelaySubscriptions(chats, time.Now())
	c.updateRelaySubscriptions(desired)
	if notice != "" && (notice != c.lastRelayNotice || viewName != c.lastRelayNoticeView) {
		c.emit(DisplayEvent{Type: "STATUS", Content: notice})
	}
	c.lastRelayNotice = notice
	c.lastRelayNoticeView = viewName
}
