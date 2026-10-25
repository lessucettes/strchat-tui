package client

import (
	"fmt"
	"slices"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

// Retry history belongs to the client loop, not to a particular socket/view.
// The bounded cache survives view switches but is never written to disk.
type relayRetry struct {
	until       time.Time
	delay       time.Duration
	since       map[string]nostr.Timestamp // immutable per-chat recovery snapshot
	activated   map[string]nostr.Timestamp // active intervals associated with the saved cursors
	lastProblem string                     // bounded warning history survives cooldown retirement
	failure     string                     // bounded diagnostic cause, not a retry policy input
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
// The per-chat pool consulted here is the cached immutable plan, so this no
// longer rebuilds and re-normalizes the relay list for every (relay, chat) pair.
func (c *client) subscriptionForRelay(url string, chats []string) relaySubscription {
	c.syncRelayPlanning()
	update := relaySubscription{
		chats: chats, since: make(map[string]nostr.Timestamp, len(chats)),
		activated: make(map[string]nostr.Timestamp, len(chats)), keys: make(map[string]string, len(chats)),
	}
	since := nostr.Now()
	for _, chat := range chats {
		if session, ok := c.chatKeys[chat]; ok {
			update.keys[session.pubKey] = session.privKey
		}
	}
	for _, chat := range chats {
		plan := c.relayPlanFor(chat)
		if plan.geo && !plan.contains(url) {
			since -= 30
			break
		}
	}
	for _, chat := range chats {
		update.since[chat] = since
		// Activation can precede worker creation when every route is cooling down.
		// Copy both the cursor and its interval; never alias the owner's map.
		if activated := c.activeChatSince[chat]; activated > 0 {
			update.activated[chat] = activated
			update.since[chat] = min(since, activated)
		}
	}
	return update.resume(relaySubscription{})
}

// Preserve each catalog's nearest five independently. Only failed primaries
// open a deficit; a healthy reserve never cancels primary recovery attempts.
func (c *client) geoReserveForChat(chat string, now time.Time) ([]string, bool) {
	c.syncRelayPlanning()
	plan := c.relayPlanFor(chat)
	lists := plan.lists
	if lists == nil {
		return nil, true
	}
	var reserves []string
	var chosen map[string]struct{}
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
				if plan.contains(url) {
					continue
				}
				if _, selected := chosen[url]; selected {
					continue
				}
				worker := c.relays[url]
				isConnected := worker != nil && worker.connected
				if isConnected == connected && c.relayEligible(url, now) {
					reserves = append(reserves, url)
					if chosen == nil {
						chosen = make(map[string]struct{})
					}
					chosen[url] = struct{}{}
					missing--
				}
			}
		}
		exhausted = exhausted || missing > 0
	}
	return reserves, exhausted
}

func (c *client) selectRelaySubscriptions(chats []string, now time.Time) (map[string][]string, string) {
	c.syncRelayPlanning()
	c.nextRelayRetry = time.Time{}
	// Also do this before retiring a view's sockets: their successful stable
	// lifetime must clear old failure penalties even if no later error occurs.
	for url, worker := range c.relays {
		if worker.connected && !worker.connectedAt.Add(c.relayTiming.stable).After(now) {
			c.relayRetries.Remove(url)
			// A stable connection ends the previous outage, so a later
			// identical auth/access failure is new information and may warn.
			worker.lastProblem = ""
		}
	}
	for chat := range c.geoPlans {
		if !slices.Contains(chats, chat) {
			delete(c.geoPlans, chat)
		}
	}
	c.pruneRelayPlans(chats)
	primary := make([][]string, len(chats))
	anchors := make([][]string, len(chats))
	reserve := make([][]string, len(chats))
	exhausted, usingReserve, unavailable := false, false, false
	for i, chat := range chats {
		plan := c.relayPlanFor(chat)
		isGeo := plan.geo
		for _, url := range plan.pool {
			if c.relayEligible(url, now) {
				if isGeo && !plan.inRendezvous(url) {
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
	// Track membership before relay eligibility: cooldown can leave no workers
	// at all. Retained group members keep their activation time; newcomers start
	// live-first, and inactive chats must not keep growing this owner-only map.
	for chat := range c.activeChatSince {
		if _, active := slices.BinarySearch(chats, chat); !active {
			delete(c.activeChatSince, chat)
		}
	}
	if len(chats) > 0 && c.activeChatSince == nil {
		c.activeChatSince = make(map[string]nostr.Timestamp, len(chats))
	}
	now := nostr.Now()
	for _, chat := range chats {
		if _, active := c.activeChatSince[chat]; !active {
			c.activeChatSince[chat] = now
		}
	}
	desired, notice := c.selectRelaySubscriptions(chats, time.Now())
	c.updateRelaySubscriptions(desired)
	health := c.logRelayHealth(viewName, chats)
	if event, log := plannerLogEvent(notice, health); log &&
		(notice != c.lastRelayNotice || viewName != c.lastRelayNoticeView) {
		if viewName != "" {
			event.Content = c.viewLogScope(viewName) + event.Content
		}
		c.emit(event)
	}
	c.lastRelayNotice = notice
	c.lastRelayNoticeView = viewName
}
