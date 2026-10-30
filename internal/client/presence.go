package client

import (
	"context"
	"math/rand/v2"
	"slices"
	"time"

	"fiatjaf.com/nostr"
)

const (
	presenceKind       = 20001
	presenceWindow     = 5 * time.Minute
	presenceChannelCap = 500
	presenceTotalCap   = 20000
)

// presenceState belongs to Run and only schedules outgoing announcements.
// Participant tracking is isolated in the activity worker.
type presenceState struct {
	chat     string
	ctx      context.Context
	cancel   context.CancelFunc
	nextSend time.Time
	lastSend time.Time // global spacing also bounds rapid destination switching
}

type relayHeartbeat struct {
	ctx   context.Context // cancelled when the active destination changes
	event nostr.Event     // immutable, already signed with that chat's identity
}

// A local group is a read aggregation, not a broadcast destination. Only its
// selected child announces presence; @mentions and highlighted previews do not.
func (c *client) syncPresenceTarget(now time.Time) {
	chat := ""
	if view := c.getActiveView(); view != nil {
		chat = view.Name
		if view.IsGroup {
			chat = view.SelectedChat
			if !slices.Contains(view.Children, chat) {
				chat = ""
				if len(view.Children) > 0 {
					chat = view.Children[0]
				}
			}
		}
	}
	p := &c.presence
	if chat == p.chat {
		return
	}
	if p.cancel != nil {
		p.cancel()
	}
	p.chat = chat
	p.ctx, p.cancel = context.WithCancel(c.ctx)
	p.nextSend = now.Add(2*time.Second + time.Duration(rand.Int64N(int64(3*time.Second))))
	if earliest := p.lastSend.Add(40 * time.Second); earliest.After(p.nextSend) {
		p.nextSend = earliest
	}
}

func (c *client) sendPresence(now time.Time) {
	p := &c.presence
	if p.chat == "" || now.Before(p.nextSend) {
		return
	}
	// No backlog and no catch-up bursts, even after a disconnected interval.
	p.nextSend = now.Add(40*time.Second + time.Duration(rand.Int64N(int64(40*time.Second))))
	session, ok := c.chatKeys[p.chat]
	if !ok {
		return
	}
	var targets []*managedRelay
	for _, worker := range c.relays {
		if worker.connected && slices.Contains(worker.chats, p.chat) && len(worker.publish) == 0 && len(worker.heartbeat) == 0 {
			targets = append(targets, worker)
		}
	}
	if len(targets) == 0 {
		return
	}
	signer, err := session.signingIdentity()
	if err != nil {
		return
	}
	tag := "d"
	if isGeohashName(p.chat) {
		tag = "g"
	}
	event := nostr.Event{Kind: presenceKind, CreatedAt: nostr.Timestamp(now.Unix()), Tags: nostr.Tags{{tag, p.chat}}, Content: ""}
	if err := signer.sign(&event); err != nil {
		return
	}
	queued := false
	for _, worker := range targets {
		select {
		case worker.heartbeat <- relayHeartbeat{ctx: p.ctx, event: event}:
			queued = true
		default:
		}
	}
	if queued {
		p.lastSend = now
		// Our process is a known local participant; relay echoes merge by key.
		c.activity.observe(p.chat, event.PubKey.Hex(), event.CreatedAt)
	}
}
