package client

import (
	"slices"
)

// Relay planning cache.
//
// The owner loop reconciles subscriptions for every relay status event, retry
// snapshot and view change. Rebuilding a chat's normalized relay pool for every
// (relay, chat) pair made that reconciliation quadratic at the safety ceiling:
// one 256-socket refresh re-parsed roughly 90,000 URLs and allocated 35 MB.
//
// A relayPlan is the immutable, normalized description of one chat's topology.
// It is computed once per anchors revision and then only read. The plan owns no
// sockets and no retry state: cooldown, reserve promotion and connection
// eligibility remain live owner decisions made during each selection pass.
type relayPlan struct {
	// revision is the planner revision the plan was built for; a stale plan is
	// never used after anchors change.
	revision uint64
	// geo reports whether the chat name is a decodable geohash. Classification
	// is fixed for a name and never depends on reply/retry state.
	geo bool
	// pool is the effective, normalized, deduplicated relay pool in the legacy
	// order: configured anchors first, then the merged primary georelays.
	pool []string
	// poolSet is the membership set of pool for O(1) subscription checks.
	poolSet map[string]struct{}
	// lists holds the per-catalog ranked georelays used for reserve selection.
	// It is nil when no catalog plan exists, which preserves the legacy
	// "incomplete coverage" result for chats without a ranked plan.
	lists [][]string
	// rendezvous is the immutable primary georelay set every bundled catalog
	// variant targets; pool entries outside it are the geo anchor tier.
	rendezvous    []string
	rendezvousSet map[string]struct{}
}

func (p *relayPlan) contains(url string) bool {
	_, ok := p.poolSet[url]
	return ok
}

func (p *relayPlan) inRendezvous(url string) bool {
	_, ok := p.rendezvousSet[url]
	return ok
}

// relayPlanner is owner-only state. Configured anchor changes are detected by
// syncRelayPlanning on every entry point. invalidateRelayPlanning is only for
// planning inputs it cannot observe: a replaced catalog/geoPlans entry or a
// test stub installed after a plan was built.
type relayPlanner struct {
	// revision advances whenever the normalized inputs change. Cached plans
	// carry the revision they were built for and are rebuilt on mismatch.
	revision uint64
	// synced records that rawAnchors reflects the configured anchor list.
	synced bool
	// rawAnchors is the configured anchor list the cache was built for.
	rawAnchors []string
	// anchors is the normalized, deduplicated anchor list for revision.
	anchors []string
	// plans is bounded to chats the selection pass has seen; inactive entries
	// are pruned like the geoPlans cache.
	plans map[string]*relayPlan
}

// syncRelayPlanning detects configured-anchor changes once per owner entry
// point. Comparing the raw list is cheap (strings compare by header) and keeps
// the cache correct for every mutation path, including direct config edits in
// tests, without re-normalizing URLs on unchanged refreshes.
func (c *client) syncRelayPlanning() {
	planner := &c.relayPlans
	if planner.synced && slices.Equal(planner.rawAnchors, c.config.AnchorRelays) {
		return
	}
	planner.synced = true
	planner.revision++
	planner.anchors = nil
	planner.rawAnchors = slices.Clone(c.config.AnchorRelays)
}

func (c *client) invalidateRelayPlanning() {
	c.relayPlans.revision++
	c.relayPlans.anchors = nil
}

// relayAnchors normalizes configured anchors once per revision. The legacy
// behavior is preserved exactly: invalid entries are skipped rather than
// failing the pool (configuration loading already rejects them), and the first
// occurrence of a duplicate wins.
func (c *client) relayAnchors() []string {
	if c.relayPlans.anchors != nil {
		return c.relayPlans.anchors
	}
	anchors := make([]string, 0, len(c.config.AnchorRelays))
	seen := make(map[string]struct{}, len(c.config.AnchorRelays))
	for _, raw := range c.config.AnchorRelays {
		url, err := normalizeRelayURL(raw)
		if err != nil {
			continue
		}
		if _, duplicate := seen[url]; duplicate {
			continue
		}
		seen[url] = struct{}{}
		anchors = append(anchors, url)
	}
	c.relayPlans.anchors = anchors
	return anchors
}

func (c *client) relayPlanFor(chat string) *relayPlan {
	planner := &c.relayPlans
	if plan, ok := planner.plans[chat]; ok && plan.revision == planner.revision {
		return plan
	}
	plan := c.buildRelayPlan(chat)
	plan.revision = planner.revision
	if planner.plans == nil {
		planner.plans = make(map[string]*relayPlan)
	}
	planner.plans[chat] = plan
	return plan
}

func (c *client) buildRelayPlan(chat string) *relayPlan {
	plan := &relayPlan{geo: isGeohashName(chat)}
	anchors := c.relayAnchors()
	rendezvousCount := 0
	if plan.geo {
		lists, ok := c.geoPlans[chat]
		if !ok {
			lists, _ = rankedGeoRelays(chat, 2*defaultRelayCount)
			c.geoPlans[chat] = lists
		}
		plan.lists = lists
		plan.rendezvous = primaryGeoRelays(lists)
		rendezvousCount = len(plan.rendezvous)
		if rendezvousCount > 0 {
			plan.rendezvousSet = make(map[string]struct{}, rendezvousCount)
		}
	} else {
		// A seeded plan entry for a non-geo chat is preserved for direct
		// geoReserveForChat callers, matching the legacy cache lookup.
		plan.lists = c.geoPlans[chat]
	}

	// The fallback applies only when the raw pool would be empty, exactly as
	// before: invalid configured entries are skipped during normalization but
	// still count as configured input.
	fallback := len(c.config.AnchorRelays) == 0 && rendezvousCount == 0
	limit := len(anchors) + rendezvousCount
	if fallback {
		limit += len(defaultEphChatRelays)
	}
	pool := make([]string, 0, limit)
	poolSet := make(map[string]struct{}, limit)
	add := func(url string) {
		if _, duplicate := poolSet[url]; duplicate {
			return
		}
		poolSet[url] = struct{}{}
		pool = append(pool, url)
	}
	for _, url := range anchors {
		add(url)
	}
	for _, raw := range plan.rendezvous {
		url, err := normalizeRelayURL(raw)
		if err != nil {
			continue
		}
		add(url)
		if plan.rendezvousSet != nil {
			plan.rendezvousSet[url] = struct{}{}
		}
	}
	if fallback {
		for _, raw := range defaultEphChatRelays {
			url, err := normalizeRelayURL(raw)
			if err != nil {
				continue
			}
			add(url)
		}
	}
	plan.pool, plan.poolSet = pool, poolSet
	return plan
}

// pruneRelayPlans keeps the planning cache bounded by the same active-chat set
// that bounds geoPlans.
func (c *client) pruneRelayPlans(chats []string) {
	for chat := range c.relayPlans.plans {
		if !slices.Contains(chats, chat) {
			delete(c.relayPlans.plans, chat)
		}
	}
}

// relayKeysMatch reports whether keys is exactly the identity snapshot
// subscriptionForRelay would build for chats. It lets a refresh recognize an
// unchanged key/chat topology without constructing a new subscription (and
// without allocating a key map). Per-chat session keys are unique by
// construction, so counting matched chats compares the same map size.
func (c *client) relayKeysMatch(keys map[string]string, chats []string) bool {
	matched := 0
	for _, chat := range chats {
		session, ok := c.chatKeys[chat]
		if !ok {
			continue
		}
		if keys[session.pubKey] != session.privKey {
			return false
		}
		matched++
	}
	return matched == len(keys)
}
