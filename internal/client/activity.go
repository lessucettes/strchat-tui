package client

import (
	"context"
	"maps"
	"sync/atomic"
	"time"

	"fiatjaf.com/nostr"
	lru "github.com/hashicorp/golang-lru/v2"
)

const (
	activityInterval        = 250 * time.Millisecond
	activityHeartbeatBudget = 64 // shared verification budget per activityInterval
)

// Run publishes immutable policy snapshots. The activity worker alone owns
// participant timestamps, heartbeat verification/deduplication and UI snapshots.
// Neither intake nor display backpressure may hold up the message pipeline.
type activityTracker struct {
	policy        atomic.Pointer[activityPolicy]
	policyChanged chan struct{}
	heartbeats    chan activityHeartbeat
	messages      chan participantActivity
}

type activityPolicy struct {
	chats   map[string]string // exact chat -> expected channel tag
	blocked map[string]bool
}

type activityHeartbeat struct {
	policy *activityPolicy
	event  *nostr.Event // immutable after the socket reader hands it off
}

type participantActivity struct {
	policy       *activityPolicy
	chat, pubkey string
	at           nostr.Timestamp
}

type activityState struct {
	policy  *activityPolicy
	members map[string]map[string]nostr.Timestamp
	entries int
	seen    *lru.Cache[nostr.ID, bool] // never evicts message replay protection
	counts  map[string]int             // immutable after publication
	dirty   bool
	pending bool
}

func newActivityTracker() *activityTracker {
	return &activityTracker{
		policyChanged: make(chan struct{}, 1),
		heartbeats:    make(chan activityHeartbeat, 64),
		messages:      make(chan participantActivity, 128),
	}
}

func (c *client) syncActivityPolicy() {
	policy := &activityPolicy{chats: make(map[string]string), blocked: make(map[string]bool)}
	if view := c.getActiveView(); view != nil {
		chats := []string{view.Name}
		if view.IsGroup {
			chats = view.Children
		}
		for _, chat := range chats {
			tag := "d"
			if isGeohashName(chat) {
				tag = "g"
			}
			policy.chats[chat] = tag
		}
	}
	for _, blocked := range c.config.BlockedUsers {
		policy.blocked[blocked.PubKey] = true
	}
	previous := c.activity.policy.Load()
	if previous != nil && maps.Equal(previous.chats, policy.chats) && maps.Equal(previous.blocked, policy.blocked) {
		return
	}
	c.activity.policy.Store(policy)
	select {
	case c.activity.policyChanged <- struct{}{}:
	default:
	}
}

// Cheap admission precedes the bounded queue; all cryptographic checks stay on
// the activity worker. A saturated queue drops telemetry, never a chat message.
func (a *activityTracker) heartbeat(event *nostr.Event) {
	if a == nil {
		return
	}
	policy := a.policy.Load()
	if !policy.acceptsHeartbeat(event, nostr.Now()) {
		return
	}
	select {
	case a.heartbeats <- activityHeartbeat{policy: policy, event: event}:
	default:
	}
}

// The caller already verified this message (or signed this local heartbeat).
// Passing only metadata avoids another signature check and retaining its text.
func (a *activityTracker) observe(chat, pubkey string, at nostr.Timestamp) {
	if a == nil {
		return
	}
	select {
	case a.messages <- participantActivity{policy: a.policy.Load(), chat: chat, pubkey: pubkey, at: at}:
	default:
	}
}

func (p *activityPolicy) acceptsHeartbeat(event *nostr.Event, now nostr.Timestamp) bool {
	if p == nil || event == nil || event.Kind != presenceKind || event.Content != "" ||
		len(event.Tags) != 1 || len(event.Tags[0]) != 2 || event.Tags[0][1] == "" ||
		event.CreatedAt > now+60 || event.CreatedAt <= now-nostr.Timestamp(presenceWindow/time.Second) {
		return false
	}
	return p.chats[event.Tags[0][1]] == event.Tags[0][0] &&
		(event.Tags[0][0] == "g" || event.Tags[0][0] == "d") && !p.blocked[event.PubKey.Hex()]
}

func newActivityState(policy *activityPolicy) *activityState {
	seen, _ := lru.New[nostr.ID, bool](seenCacheSize)
	return &activityState{policy: policy, seen: seen}
}

func (a *activityTracker) run(ctx context.Context, events chan<- DisplayEvent) {
	ticker := time.NewTicker(activityInterval)
	defer ticker.Stop()
	state := newActivityState(a.policy.Load())
	nextSweep := time.Now().Add(10 * time.Second)
	budget := activityHeartbeatBudget
	for ctx.Err() == nil {
		var heartbeats <-chan activityHeartbeat
		if budget > 0 {
			heartbeats = a.heartbeats
		}
		select {
		case <-ctx.Done():
			return
		case <-a.policyChanged:
			state.setPolicy(a.policy.Load())
		case message := <-a.messages:
			state.setPolicy(a.policy.Load())
			if message.policy == state.policy {
				state.remember(message.chat, message.pubkey, message.at, nostr.Now())
			}
		case heartbeat := <-heartbeats:
			budget--
			state.setPolicy(a.policy.Load())
			if heartbeat.policy == state.policy {
				state.heartbeat(heartbeat.event, nostr.Now())
			}
		case <-ticker.C:
			state.setPolicy(a.policy.Load())
			budget = activityHeartbeatBudget
			now := time.Now()
			if !now.Before(nextSweep) {
				state.expire(nostr.Timestamp(now.Unix()))
				nextSweep = now.Add(10 * time.Second)
			}
			state.refreshCounts()
			if state.pending {
				select {
				case events <- DisplayEvent{Type: "PRESENCE_UPDATE", Payload: state.counts}:
					state.pending = false
				default:
				}
			}
		}
	}
}

func (s *activityState) heartbeat(event *nostr.Event, now nostr.Timestamp) {
	if !s.policy.acceptsHeartbeat(event, now) || s.seen.Contains(event.ID) {
		return
	}
	if _, _, ok := verifyEventDigest(event, nil); !ok {
		return
	}
	s.seen.Add(event.ID, true)
	s.remember(event.Tags[0][1], event.PubKey.Hex(), event.CreatedAt, now)
}

func (s *activityState) remember(chat, pubkey string, at, now nostr.Timestamp) {
	at = min(at, now)
	if s.policy == nil || s.policy.chats[chat] == "" || s.policy.blocked[pubkey] || at <= now-nostr.Timestamp(presenceWindow/time.Second) {
		return
	}
	members := s.members[chat]
	previous, known := members[pubkey]
	if known && previous >= at {
		return
	}
	if !known && (len(members) >= presenceChannelCap || s.entries >= presenceTotalCap) {
		return
	}
	if members == nil {
		members = make(map[string]nostr.Timestamp)
		if s.members == nil {
			s.members = make(map[string]map[string]nostr.Timestamp)
		}
		s.members[chat] = members
	}
	members[pubkey] = at
	if !known {
		s.entries++
		s.dirty = true
	}
}

func (s *activityState) setPolicy(policy *activityPolicy) {
	if policy == s.policy {
		return
	}
	s.policy = policy
	for chat, members := range s.members {
		if policy == nil || policy.chats[chat] == "" {
			s.entries -= len(members)
			delete(s.members, chat)
			s.dirty = true
			continue
		}
		for pubkey := range members {
			if policy.blocked[pubkey] {
				delete(members, pubkey)
				s.entries--
				s.dirty = true
			}
		}
		if len(members) == 0 {
			delete(s.members, chat)
		}
	}
}

// Hard entry caps bound sweep work, independently of message processing.
func (s *activityState) expire(now nostr.Timestamp) {
	for chat, members := range s.members {
		for pubkey, at := range members {
			if at <= now-nostr.Timestamp(presenceWindow/time.Second) {
				delete(members, pubkey)
				s.entries--
				s.dirty = true
			}
		}
		if len(members) == 0 {
			delete(s.members, chat)
		}
	}
}

func (s *activityState) refreshCounts() {
	if !s.dirty {
		return
	}
	s.dirty = false
	counts := make(map[string]int, len(s.members))
	for chat, members := range s.members {
		if len(members) > 0 {
			counts[chat] = len(members)
		}
	}
	if !maps.Equal(counts, s.counts) {
		s.counts = counts
		s.pending = true
	}
}
