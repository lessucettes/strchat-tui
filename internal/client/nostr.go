package client

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mmcloughlin/geohash"
	"github.com/nbd-wtf/go-nostr"
	"github.com/rivo/uniseg"
)

// Validate before caching: forged IDs must not poison duplicate suppression.
// The 64 KiB wire cap also bounds JSON decoding and signature verification input.
func (c *client) processEvent(ev *nostr.Event, relayURL string) {
	if ev == nil || len(ev.ID) != 64 || len(ev.PubKey) != 64 || len(ev.Sig) != 128 || len(ev.Content) > maxContentBytes || len(ev.Tags) > 128 || !utf8.ValidString(ev.Content) {
		return
	}
	if c.seenCache.Contains(ev.ID) {
		return
	}
	tagKey := "d"
	switch ev.Kind {
	case geoChatKind:
		tagKey = "g"
	case ephChatKind:
	default:
		return
	}
	tag := ev.Tags.Find(tagKey)
	if len(tag) < 2 {
		return
	}
	chat := tag[1]
	active := c.getActiveView()
	if active == nil || (!active.IsGroup && active.Name != chat) || (active.IsGroup && !slices.Contains(active.Children, chat)) {
		return
	}
	if (ev.Kind == geoChatKind) != (geohash.Validate(chat) == nil) {
		return
	}
	now := nostr.Now()
	if ev.CreatedAt > now+60 || ev.CreatedAt < now-300 {
		return
	}
	if expiry := ev.Tags.Find("expiration"); len(expiry) > 1 {
		timestamp, err := strconv.ParseInt(expiry[1], 10, 64)
		if err != nil || timestamp <= int64(now) {
			return
		}
	}
	for _, blocked := range c.config.BlockedUsers {
		if blocked.PubKey == ev.PubKey {
			return
		}
	}
	if !ev.CheckID() {
		return
	}
	if valid, _ := ev.CheckSignature(); !valid {
		return
	}
	if !isPoWValid(ev, c.effectivePoWForChat(chat)) {
		return
	}
	c.seenCache.Add(ev.ID, true)
	content := sanitizeString(truncateString(ev.Content, MaxMsgLen))
	if c.matchesAny(content, c.mutesCompiled) || (len(c.filtersCompiled) > 0 && !c.matchesAny(content, c.filtersCompiled)) {
		return
	}
	nick := npubToTokiPona(ev.PubKey)
	short := ev.PubKey[:4]
	if tag := ev.Tags.Find("n"); len(tag) > 1 {
		if text := strings.TrimSpace(sanitizeString(truncateString(tag[1], 64))); text != "" {
			nick = text
		}
		short = safeSuffix(ev.PubKey, 4)
	}
	c.userContext.Add(ev.PubKey, userContext{nick: nick, chat: chat, shortPubKey: short})
	own := ev.PubKey == c.pk
	for _, session := range c.chatKeys {
		if session.pubKey == ev.PubKey {
			own = true
			break
		}
	}
	c.emit(DisplayEvent{Type: "NEW_MESSAGE", Timestamp: time.Unix(int64(ev.CreatedAt), 0).Format("15:04:05"), Nick: nick,
		FullPubKey: ev.PubKey, ShortPubKey: short, IsOwnMessage: own, Content: content, ID: safeSuffix(ev.ID, 4), Chat: chat, RelayURL: relayURL})
}

const maxContentBytes = 16 << 10

type publishJob struct {
	event      nostr.Event
	key, chat  string
	difficulty int
	relays     []*managedRelay
}

type publishResult struct {
	event         nostr.Event
	accepted      int
	kind, message string
}

// Resolve chat, key, nickname, PoW and relay targets on the owner loop. Changing
// views while mining or publishing cannot change an already queued message.
func (c *client) publishMessage(message string) {
	if strings.TrimSpace(message) == "" {
		return
	}
	if !utf8.ValidString(message) || len(message) > maxContentBytes || uniseg.GraphemeClusterCount(message) > MaxMsgLen {
		c.emit(DisplayEvent{Type: "ERROR", Content: "Message exceeds 2000 characters or 16 KiB."})
		return
	}
	view := c.getActiveView()
	if view == nil {
		c.emit(DisplayEvent{Type: "ERROR", Content: "No active chat."})
		return
	}
	chat := view.Name
	var target, matched string
	if strings.HasPrefix(message, "@") {
		for _, pk := range c.userContext.Keys() {
			user, _ := c.userContext.Peek(pk)
			prefix := fmt.Sprintf("@%s#%s", user.nick, user.shortPubKey)
			if strings.HasPrefix(message, prefix) && (len(message) == len(prefix) || message[len(prefix)] == ' ') && len(prefix) > len(matched) {
				target, chat, matched = pk, user.chat, prefix
			}
		}
		if target == "" {
			c.emit(DisplayEvent{Type: "ERROR", Content: "Unknown recipient. Use nickname completion."})
			return
		}
	}
	if view.IsGroup && target == "" {
		c.emit(DisplayEvent{Type: "ERROR", Content: "Select a chat or use @nick to reply from a local group."})
		return
	}
	if (!view.IsGroup && view.Name != chat) || (view.IsGroup && !slices.Contains(view.Children, chat)) {
		c.emit(DisplayEvent{Type: "ERROR", Content: "Recipient is not in the active chat/group."})
		return
	}
	job := publishJob{key: c.sk, chat: chat, difficulty: c.effectivePoWForChat(chat)}
	nick := c.config.Nick
	if !view.IsGroup {
		if session, ok := c.chatKeys[chat]; ok {
			job.key = session.privKey
			nick = session.nick
		}
	}
	pk, err := nostr.GetPublicKey(job.key)
	if err != nil {
		c.emit(DisplayEvent{Type: "ERROR", Content: "No valid signing identity."})
		return
	}
	if nick == "" {
		nick = npubToTokiPona(pk)
	}
	kind, tag := ephChatKind, "d"
	if geohash.Validate(chat) == nil {
		kind, tag = geoChatKind, "g"
	}
	job.event = nostr.Event{PubKey: pk, Kind: kind, CreatedAt: nostr.Now(), Content: message, Tags: nostr.Tags{{tag, chat}, {"n", nick}}}
	if target != "" {
		job.event.Tags = append(job.event.Tags, nostr.Tag{"p", target})
	}
	for _, worker := range c.relays {
		if worker.connected && slices.Contains(worker.chats, chat) {
			job.relays = append(job.relays, worker)
		}
	}
	if len(job.relays) == 0 {
		c.emit(DisplayEvent{Type: "ERROR", Content: "No connected relay for this chat. Message was not queued; retry after reconnect."})
		return
	}
	select {
	case c.outgoing <- job:
	default:
		c.emit(DisplayEvent{Type: "ERROR", Content: "Send queue is full. Message was not queued; retry later."})
	}
}

func (c *client) publishLoop() {
	for {
		select {
		case <-c.ctx.Done():
			return
		case job := <-c.outgoing:
			result := executePublish(c.ctx, job)
			select {
			case c.results <- result:
			case <-c.ctx.Done():
				return
			}
		}
	}
}

func executePublish(ctx context.Context, job publishJob) publishResult {
	result := publishResult{kind: "ERROR"}
	// Mining and publishing share one deadline and one worker, not one CPU loop per
	// keystroke. Work not acknowledged is never retried automatically.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	event := job.event
	event.CreatedAt = nostr.Now()
	if job.difficulty > 0 {
		event.Tags = append(event.Tags, nostr.Tag{"nonce", "0", strconv.Itoa(job.difficulty)})
		for nonce := uint64(0); ; nonce++ {
			if nonce&1023 == 0 && ctx.Err() != nil {
				result.message = "Proof-of-work timed out or was cancelled; message not sent."
				return result
			}
			event.Tags[len(event.Tags)-1][1] = strconv.FormatUint(nonce, 10)
			if countLeadingZeroBits(event.GetID()) >= job.difficulty {
				break
			}
		}
	}
	if err := event.Sign(job.key); err != nil {
		result.message = "Failed to sign message."
		return result
	}
	result.event = event
	// Dispatch in parallel via existing relay workers; a slow relay does not delay
	// sending to a healthy one and creates no additional publishing goroutines.
	answers := make([]chan error, 0, len(job.relays))
	for _, worker := range job.relays {
		answer := make(chan error, 1)
		request := relayPublish{ctx: ctx, event: event, result: answer}
		select {
		case worker.publish <- request:
			answers = append(answers, answer)
		default: // Worker already has pending work; count as unacknowledged.
		}
	}
	var failure string
	for _, answer := range answers {
		var err error
		select {
		case err = <-answer:
		case <-ctx.Done():
			// An earlier slow relay must not hide an answer already received
			// from a healthy peer when the deadline becomes ready as well.
			select {
			case err = <-answer:
			default:
				err = ctx.Err()
			}
		}
		if err == nil {
			result.accepted++
		} else if failure == "" {
			failure = err.Error()
		}
	}
	if result.accepted > 0 {
		result.kind = "STATUS"
	}
	result.message = fmt.Sprintf("Event %s acknowledged by %d/%d relays for %s.", safeSuffix(event.ID, 4), result.accepted, len(job.relays), job.chat)
	if result.accepted < len(job.relays) {
		result.message += " Delivery to other relays is unconfirmed; no automatic resend."
		if failure != "" {
			result.message += " " + failure
		}
	}
	return result
}

func (c *client) effectivePoWForChat(chat string) int {
	for _, view := range c.config.Views {
		if !view.IsGroup && view.Name == chat && view.PoW > 0 {
			return view.PoW
		}
	}
	if view := c.getActiveView(); view != nil && view.IsGroup && slices.Contains(view.Children, chat) {
		return view.PoW
	}
	return 0
}
