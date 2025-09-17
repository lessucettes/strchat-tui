// client/nostr.go
package client

import (
	"context"
	"fmt"
	"log"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mmcloughlin/geohash"
	"github.com/nbd-wtf/go-nostr"
)

// --- Retry helper ---

func retryWithBackoff(ctx context.Context, fn func() error) error {
	delay := 500 * time.Millisecond
	for {
		if err := fn(); err == nil {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
			if delay < 30*time.Second {
				delay *= 2
			}
		}
	}
}

// --- Nostr Logic ---

func (c *Client) updateAllSubscriptions() {
	activeView := c.getActiveView()

	activeChats := make(map[string]struct{})
	if activeView != nil {
		if activeView.IsGroup {
			for _, child := range activeView.Children {
				activeChats[child] = struct{}{}
			}
		} else if activeView.Name != "" {
			activeChats[activeView.Name] = struct{}{}
		}
	}

	if len(activeChats) == 0 {
		c.updateRelaySubscriptions(make(map[string][]string))
		c.eventsChan <- DisplayEvent{Type: "STATUS", Content: "No active chat/group. Relay connections are inactive."}
		return
	}

	c.eventsChan <- DisplayEvent{Type: "STATUS", Content: "Updating subscriptions for active chat/group..."}

	desiredRelayToChats := make(map[string][]string)
	for chat := range activeChats {
		var relayURLs []string
		if geohash.Validate(chat) == nil {
			var err error
			relayURLs, err = ClosestRelays(chat, DefaultRelayCount)
			if err != nil || len(relayURLs) == 0 {
				relayURLs = DefaultNamedChatRelays
			}
		} else {
			relayURLs = DefaultNamedChatRelays
		}
		for _, url := range relayURLs {
			found := false
			for _, existingChat := range desiredRelayToChats[url] {
				if existingChat == chat {
					found = true
					break
				}
			}
			if !found {
				desiredRelayToChats[url] = append(desiredRelayToChats[url], chat)
			}
		}
	}

	c.updateRelaySubscriptions(desiredRelayToChats)
}

func (c *Client) updateRelaySubscriptions(desiredRelays map[string][]string) {
	c.relaysMu.Lock()
	currentRelays := make(map[string]*ManagedRelay, len(c.relays))
	maps.Copy(currentRelays, c.relays)
	c.relaysMu.Unlock()
	var wg sync.WaitGroup
	for url, chats := range desiredRelays {
		if mr, exists := currentRelays[url]; exists {
			wg.Add(1)
			go func(mr *ManagedRelay, chats []string) {
				defer wg.Done()
				if _, err := c.replaceSubscription(mr, chats); err != nil {
					c.eventsChan <- DisplayEvent{
						Type:    "ERROR",
						Content: fmt.Sprintf("Resubscribe failed on %s: %v", mr.URL, err),
					}
				}
			}(mr, chats)
		} else {
			wg.Add(1)
			go func(url string, chats []string) {
				defer wg.Done()
				c.manageRelayConnection(url, chats)
			}(url, chats)
		}
	}
	c.relaysMu.Lock()
	for url, mr := range c.relays {
		if _, needed := desiredRelays[url]; !needed {
			log.Printf("Disconnecting from unneeded relay: %s", url)
			mr.mu.Lock()
			if mr.subscription != nil {
				mr.subscription.Unsub()
				mr.subscription = nil
			}
			if mr.Relay != nil {
				mr.Relay.Close()
			}
			mr.mu.Unlock()
			delete(c.relays, url)
		}
	}
	c.relaysMu.Unlock()
	wg.Wait()
	c.sendRelaysUpdate()
}

func (c *Client) manageRelayConnection(url string, chats []string) {
	err := retryWithBackoff(c.ctx, func() error {
		start := time.Now()
		relay, err := nostr.RelayConnect(c.ctx, url)
		if err != nil {
			c.eventsChan <- DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("Failed to connect to %s: %v", url, err)}
			return err
		}
		latency := time.Since(start)

		c.eventsChan <- DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("Connected to %s (%dms)", url, latency.Milliseconds())}

		mr := &ManagedRelay{
			URL:     url,
			Relay:   relay,
			Latency: latency,
		}

		c.relaysMu.Lock()
		if _, exists := c.relays[url]; exists {
			c.relaysMu.Unlock()
			relay.Close()
			return nil
		}
		c.relays[url] = mr
		c.relaysMu.Unlock()
		c.sendRelaysUpdate()

		if _, err := c.replaceSubscription(mr, chats); err != nil {
			c.eventsChan <- DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("Initial subscription to %s failed.", url)}
			relay.Close()
			c.relaysMu.Lock()
			delete(c.relays, url)
			c.relaysMu.Unlock()
			c.sendRelaysUpdate()
			return err
		}

		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			c.listenForEvents(mr)
		}()

		return nil
	})

	if err != nil {
		log.Printf("Stopped trying to connect to %s: %v", url, err)
	}
}

func (c *Client) replaceSubscription(mr *ManagedRelay, chats []string) (bool, error) {
	mr.mu.Lock()
	oldChats := mrCurrentChatsLocked(mr.subscription)
	mr.mu.Unlock()

	if sameStringSet(oldChats, chats) {
		return false, nil
	}

	now := nostr.Now()
	filters := make(nostr.Filters, 0, len(chats))
	for _, ch := range chats {
		since := now
		if geohash.Validate(ch) == nil {
			filters = append(filters, nostr.Filter{
				Kinds: []int{GeochatKind},
				Tags:  nostr.TagMap{"g": []string{ch}},
				Since: &since,
			})
		} else {
			filters = append(filters, nostr.Filter{
				Kinds: []int{NamedChatKind},
				Tags:  nostr.TagMap{"d": []string{ch}},
				Since: &since,
			})
		}
	}

	newSub, err := mr.Relay.Subscribe(c.ctx, filters)
	if err != nil {
		return false, fmt.Errorf("subscribe failed: %w", err)
	}

	mr.mu.Lock()
	oldSub := mr.subscription
	mr.subscription = newSub
	mr.mu.Unlock()

	if oldSub != nil {
		oldSub.Unsub()
	}
	log.Printf("Updated subscription for %s with %d chat(s)", mr.URL, len(chats))

	c.sendRelaysUpdate()

	return true, nil
}

func (c *Client) listenForEvents(mr *ManagedRelay) {
	log.Printf("Listener started for relay: %s", mr.URL)
	defer log.Printf("Listener stopped for relay: %s", mr.URL)

	for {
		if c.ctx.Err() != nil {
			return
		}

		mr.mu.Lock()
		sub := mr.subscription
		mr.mu.Unlock()

		if sub == nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}

		select {
		case <-c.ctx.Done():
			return

		case ev, ok := <-sub.Events:
			if !ok {
				c.eventsChan <- DisplayEvent{Type: "STATUS",
					Content: fmt.Sprintf("Subscription closed on %s, attempting to resubscribe...", mr.URL)}
				oldChats := mrCurrentChatsLocked(sub)

				mr.mu.Lock()
				if mr.subscription != sub {
					mr.mu.Unlock()
					continue
				}
				mr.subscription = nil
				mr.mu.Unlock()

				if len(oldChats) == 0 {
					continue
				}

				err := retryWithBackoff(c.ctx, func() error {
					_, err := c.replaceSubscription(mr, oldChats)
					return err
				})

				if err != nil {
					c.eventsChan <- DisplayEvent{Type: "ERROR",
						Content: fmt.Sprintf("Could not re-establish subscription on %s. Listener stopped.", mr.URL)}
					return
				}
				c.eventsChan <- DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("Successfully reconnected to %s!", mr.URL)}
				continue
			}
			if ev == nil {
				continue
			}
			c.processEvent(ev, mr.URL)
		}
	}
}

func (c *Client) processEvent(ev *nostr.Event, relayURL string) {
	for _, blockedUser := range c.config.BlockedUsers {
		if ev.PubKey == blockedUser.PubKey {
			return
		}
	}

	c.seenCacheMu.Lock()
	if c.seenCache.Contains(ev.ID) {
		c.seenCacheMu.Unlock()
		return
	}
	c.seenCache.Add(ev.ID, true)
	c.seenCacheMu.Unlock()

	var eventChat string
	if gTag := ev.Tags.Find("g"); len(gTag) > 1 {
		eventChat = gTag[1]
	} else if dTag := ev.Tags.Find("d"); len(dTag) > 1 {
		eventChat = dTag[1]
	}

	if eventChat == "" {
		return
	}

	activeView := c.getActiveView()
	if activeView != nil {
		isRelevantToActiveView := false
		if activeView.IsGroup {
			if slices.Contains(activeView.Children, eventChat) {
				isRelevantToActiveView = true
			}
		} else {
			if activeView.Name == eventChat {
				isRelevantToActiveView = true
			}
		}

		if isRelevantToActiveView {
			requiredPoW := activeView.PoW
			if !IsPoWValid(ev, requiredPoW) {
				log.Printf("Dropped event %s from %s for failing PoW check (required: %d)", ev.ID[len(ev.ID)-4:], eventChat, requiredPoW)
				return
			}
		}
	}

	content := truncateString(ev.Content, MaxMsgLen)
	content = sanitizeString(content)

	if c.matchesAny(content, c.mutesCompiled) {
		return
	}
	if len(c.filtersCompiled) > 0 && !c.matchesAny(content, c.filtersCompiled) {
		return
	}

	nick := npubToTokiPona(ev.PubKey)
	spk := ev.PubKey[:4]
	if nickTag := ev.Tags.Find("n"); len(nickTag) > 1 {
		if s := sanitizeString(nickTag[1]); s != "" {
			nick = s
		}
		spk = ev.PubKey[len(ev.PubKey)-4:]
	}

	c.userContext.Add(ev.PubKey, UserContext{
		Nick:        nick,
		Chat:        eventChat,
		ShortPubKey: spk,
	})

	timestamp := time.Unix(int64(ev.CreatedAt), 0).Format("15:04:05")

	isOwn := ev.PubKey == c.pk

	c.eventsChan <- DisplayEvent{
		Type:         "NEW_MESSAGE",
		Timestamp:    timestamp,
		Nick:         nick,
		FullPubKey:   ev.PubKey,
		ShortPubKey:  spk,
		IsOwnMessage: isOwn,
		Content:      content,
		ID:           ev.ID[len(ev.ID)-4:],
		Chat:         eventChat,
		RelayURL:     relayURL,
	}
}

func (c *Client) publishMessage(message string) {
	var targetChat string
	var targetPubKey string
	if strings.HasPrefix(message, "@") {
		var matchedReplyTag string
		for _, pk := range c.userContext.Keys() {
			if ctx, ok := c.userContext.Get(pk); ok {
				replyTag := fmt.Sprintf("@%s#%s", ctx.Nick, ctx.ShortPubKey)
				if strings.HasPrefix(message, replyTag) {
					if len(replyTag) > len(matchedReplyTag) {
						matchedReplyTag = replyTag
						targetPubKey = pk
						targetChat = ctx.Chat
					}
				}
			}
		}

		if targetPubKey == "" {
			c.eventsChan <- DisplayEvent{Type: "ERROR", Content: "Could not find a known user matching your message prefix."}
			return
		}
	} else {
		activeView := c.getActiveView()
		if activeView == nil {
			c.eventsChan <- DisplayEvent{Type: "ERROR", Content: "No active chat/group to send message to."}
			return
		}
		if activeView.IsGroup {
			c.eventsChan <- DisplayEvent{Type: "ERROR", Content: "Broadcasting to a group is disabled. Use @nick to send a message."}
			return
		}
		if activeView.Name == "" {
			c.eventsChan <- DisplayEvent{Type: "ERROR", Content: "The active chat is invalid."}
			return
		}
		targetChat = activeView.Name
	}

	var kind int
	var tagKey string
	var relayURLs []string
	if geohash.Validate(targetChat) == nil {
		kind = GeochatKind
		tagKey = "g"
		var err error
		relayURLs, err = ClosestRelays(targetChat, DefaultRelayCount)
		if err != nil || len(relayURLs) == 0 {
			c.eventsChan <- DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("No relays found for chat %s", targetChat)}
			return
		}
	} else {
		kind = NamedChatKind
		tagKey = "d"
		relayURLs = DefaultNamedChatRelays
	}

	tags := nostr.Tags{{tagKey, targetChat}}
	if targetPubKey != "" {
		tags = append(tags, nostr.Tag{"p", targetPubKey})
	}

	activeView := c.getActiveView()
	if activeView == nil {
		c.eventsChan <- DisplayEvent{Type: "ERROR", Content: "Cannot determine PoW: No active chat/group."}
		return
	}
	requiredPoW := activeView.PoW

	c.relaysMu.Lock()
	var relaysForPublishing []*ManagedRelay
	for _, url := range relayURLs {
		if r, ok := c.relays[url]; ok {
			relaysForPublishing = append(relaysForPublishing, r)
		}
	}
	c.relaysMu.Unlock()

	if len(relaysForPublishing) == 0 {
		c.eventsChan <- DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Not connected to any suitable relays for chat %s", targetChat)}
		return
	}

	ev := c.createEvent(message, kind, tags, requiredPoW)

	if requiredPoW > 0 {
		go c.minePoWAndPublish(ev, requiredPoW, targetChat, relaysForPublishing)
	} else {
		_ = ev.Sign(c.sk)
		c.publish(ev, targetChat, relaysForPublishing)
	}
}

func (c *Client) minePoWAndPublish(ev nostr.Event, difficulty int, targetChat string, relays []*ManagedRelay) {
	clone := ev

	c.eventsChan <- DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("Calculating Proof-of-Work (difficulty %d)...", difficulty)}

	nonceTagIndex := -1
	for i, tag := range clone.Tags {
		if len(tag) > 1 && tag[0] == "nonce" {
			nonceTagIndex = i
			break
		}
	}

	if nonceTagIndex == -1 {
		c.eventsChan <- DisplayEvent{Type: "ERROR", Content: "PoW mining failed: nonce tag not found."}
		return
	}

	var nonceCounter uint64 = 0
	for {
		clone.Tags[nonceTagIndex][1] = strconv.FormatUint(nonceCounter, 10)
		clone.ID = clone.GetID()

		if countLeadingZeroBits(clone.ID) >= difficulty {
			break
		}
		nonceCounter++

		if nonceCounter&0x3FF == 0 {
			select {
			case <-c.ctx.Done():
				c.eventsChan <- DisplayEvent{Type: "STATUS", Content: "PoW calculation cancelled."}
				return
			default:
			}
		}
	}

	_ = clone.Sign(c.sk)
	c.publish(clone, targetChat, relays)
}

func (c *Client) publish(ev nostr.Event, targetChat string, relaysForPublishing []*ManagedRelay) {
	sort.Slice(relaysForPublishing, func(i, j int) bool {
		return relaysForPublishing[i].Latency < relaysForPublishing[j].Latency
	})

	var wg sync.WaitGroup
	successCount := 0
	var errorMessages []string
	var mu sync.Mutex

	for _, r := range relaysForPublishing {
		wg.Add(1)
		go func(r *ManagedRelay) {
			defer wg.Done()
			if err := r.Relay.Publish(c.ctx, ev); err == nil {
				mu.Lock()
				successCount++
				mu.Unlock()
			} else {
				mu.Lock()
				errorMessages = append(errorMessages, fmt.Sprintf("%s: %v", r.URL, err))
				mu.Unlock()
			}
		}(r)
	}
	wg.Wait()

	status := fmt.Sprintf("Event %s sent to %d/%d relays for chat %s.", ev.ID[len(ev.ID)-4:], successCount, len(relaysForPublishing), targetChat)
	if len(errorMessages) > 0 {
		status += " Errors: " + strings.Join(errorMessages, ", ")
	}
	c.eventsChan <- DisplayEvent{Type: "STATUS", Content: status}
}

func (c *Client) createEvent(message string, kind int, tags nostr.Tags, difficulty int) nostr.Event {
	baseTags := make(nostr.Tags, 0, len(tags)+2)
	baseTags = append(baseTags, tags...)
	baseTags = append(baseTags, nostr.Tag{"n", c.n})

	ev := nostr.Event{
		CreatedAt: nostr.Now(),
		PubKey:    c.pk,
		Content:   message,
		Kind:      kind,
		Tags:      baseTags,
	}

	if difficulty > 0 {
		ev.Tags = append(ev.Tags, nostr.Tag{"nonce", "0", strconv.Itoa(difficulty)})
	}

	return ev
}

func (c *Client) sendRelaysUpdate() {
	c.relaysMu.Lock()
	defer c.relaysMu.Unlock()

	statuses := make([]RelayInfo, 0, len(c.relays))
	for _, mr := range c.relays {
		statuses = append(statuses, RelayInfo{
			URL:     mr.URL,
			Latency: mr.Latency,
		})
	}

	c.eventsChan <- DisplayEvent{Type: "RELAYS_UPDATE", Payload: statuses}
}
