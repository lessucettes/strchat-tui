package client

import (
	"context"
	"fmt"
	"log"
	"maps"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mmcloughlin/geohash"
	"github.com/nbd-wtf/go-nostr"
)

// --- Nostr Logic ---

func (c *client) getRelayPoolForChat(chat string) []string {
	relaySet := make(map[string]struct{})

	for _, url := range c.config.AnchorRelays {
		relaySet[url] = struct{}{}
	}

	if c.discoveredStore != nil {
		for _, url := range c.getDiscoveredRelayURLs() {
			relaySet[url] = struct{}{}
		}
	}

	if geohash.Validate(chat) == nil {
		closest, err := closestRelays(chat, defaultRelayCount)
		if err == nil {
			for _, url := range closest {
				relaySet[url] = struct{}{}
			}
		}
	}

	relayURLs := make([]string, 0, len(relaySet))
	for url := range relaySet {
		relayURLs = append(relayURLs, url)
	}

	if len(relayURLs) == 0 {
		relayURLs = defaultNamedChatRelays
	}

	return relayURLs
}

func (c *client) updateAllSubscriptions() {
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
		relayURLs := c.getRelayPoolForChat(chat)
		for _, url := range relayURLs {
			found := slices.Contains(desiredRelayToChats[url], chat)
			if !found {
				desiredRelayToChats[url] = append(desiredRelayToChats[url], chat)
			}
		}
	}

	c.updateRelaySubscriptions(desiredRelayToChats)
}

func (c *client) updateRelaySubscriptions(desiredRelays map[string][]string) {
	c.relaysMu.Lock()
	currentRelays := make(map[string]*managedRelay, len(c.relays))
	maps.Copy(currentRelays, c.relays)
	c.relaysMu.Unlock()

	var wg sync.WaitGroup
	for url, chats := range desiredRelays {

		if c.relayFailed(url) {
			continue
		}

		if mr, exists := currentRelays[url]; exists {
			wg.Add(1)
			go func(mr *managedRelay, chats []string) {
				defer wg.Done()
				if _, err := c.replaceSubscription(mr, chats); err != nil {
					c.eventsChan <- DisplayEvent{
						Type:    "ERROR",
						Content: fmt.Sprintf("Resubscribe failed on %s: %v", mr.url, err),
					}

					c.markRelayFailed(url)
				}
			}(mr, chats)
		} else {
			go c.manageRelayConnection(url, chats)
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
			if mr.relay != nil {
				mr.relay.Close()
			}
			mr.mu.Unlock()
			delete(c.relays, url)
		}
	}
	c.relaysMu.Unlock()

	wg.Wait()
	c.sendRelaysUpdate()
}

func (c *client) manageRelayConnection(url string, chats []string) {
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()

	if c.relayFailed(url) {
		c.eventsChan <- DisplayEvent{
			Type:    "STATUS",
			Content: fmt.Sprintf("Skipping connect to discovered relay %s (in fail cache)", url),
		}
		return
	}

	start := time.Now()
	relay, err := nostr.RelayConnect(ctx, url)
	if err != nil {
		if !c.isDiscoveredRelay(url) {
			c.eventsChan <- DisplayEvent{
				Type:    "ERROR",
				Content: fmt.Sprintf("Failed to connect to %s: %v", url, err),
			}
		}

		c.markRelayFailed(url)
		return
	}
	latency := time.Since(start)

	mr := &managedRelay{
		url:               url,
		relay:             relay,
		latency:           latency,
		connected:         true,
		reconnectAttempts: 0,
	}

	c.relaysMu.Lock()
	if _, exists := c.relays[url]; exists {
		c.relaysMu.Unlock()
		relay.Close()
		return
	}
	c.relays[url] = mr
	c.relaysMu.Unlock()
	c.sendRelaysUpdate()

	if _, err := c.replaceSubscription(mr, chats); err != nil {
		if c.isDiscoveredRelay(mr.url) && c.verifyFailCache != nil {
			c.markRelayFailed(mr.url)
		}

		relay.Close()
		c.relaysMu.Lock()
		delete(c.relays, mr.url)
		c.relaysMu.Unlock()
		c.sendRelaysUpdate()
		return
	}

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.listenForEvents(mr)
	}()
}

func (c *client) replaceSubscription(mr *managedRelay, chats []string) (bool, error) {
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
				Kinds: []int{geochatKind},
				Tags:  nostr.TagMap{"g": []string{ch}},
				Since: &since,
			})
		} else {
			filters = append(filters, nostr.Filter{
				Kinds: []int{namedChatKind},
				Tags:  nostr.TagMap{"d": []string{ch}},
				Since: &since,
			})
		}
	}

	newSub, err := mr.relay.Subscribe(c.ctx, filters)
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
	log.Printf("Updated subscription for %s with %d chat(s)", mr.url, len(chats))

	c.sendRelaysUpdate()

	return true, nil
}

func (c *client) listenForEvents(mr *managedRelay) {
	log.Printf("Listener started for relay: %s", mr.url)
	defer log.Printf("Listener stopped for relay: %s", mr.url)

	const maxReconnectAttempts = 3

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
				oldChats := mrCurrentChatsLocked(sub)

				mr.mu.Lock()
				if mr.subscription != sub {
					mr.mu.Unlock()
					continue
				}
				mr.subscription = nil
				mr.connected = false
				mr.mu.Unlock()

				c.sendRelaysUpdate()

				c.discoveredStore.mu.RLock()
				_, isDiscovered := c.discoveredStore.Relays[mr.url]
				c.discoveredStore.mu.RUnlock()

				if isDiscovered {
					c.relaysMu.Lock()
					delete(c.relays, mr.url)
					c.relaysMu.Unlock()

					if c.verifyFailCache != nil {
						c.markRelayFailed(mr.url)
					}
					c.sendRelaysUpdate()
					return
				}

				if len(oldChats) == 0 {
					c.relaysMu.Lock()
					delete(c.relays, mr.url)
					c.relaysMu.Unlock()
					c.sendRelaysUpdate()
					return
				}

				mr.mu.Lock()
				mr.reconnectAttempts++
				attempts := mr.reconnectAttempts
				mr.mu.Unlock()

				if attempts > maxReconnectAttempts {
					c.eventsChan <- DisplayEvent{
						Type:    "ERROR",
						Content: fmt.Sprintf("Anchor/Geo relay %s failed to reconnect after %d attempts. Giving up.", mr.url, maxReconnectAttempts),
					}

					c.relaysMu.Lock()
					delete(c.relays, mr.url)
					c.relaysMu.Unlock()
					c.sendRelaysUpdate()
					return
				}

				err := retryWithBackoff(c.ctx, func() error {
					_, err := c.replaceSubscription(mr, oldChats)
					return err
				}, attempts)

				if err != nil {
					c.eventsChan <- DisplayEvent{
						Type:    "ERROR",
						Content: fmt.Sprintf("Could not re-establish subscription on %s (attempt %d). Error: %v. Listener stopped.", mr.url, attempts, err),
					}
					c.relaysMu.Lock()
					delete(c.relays, mr.url)
					c.relaysMu.Unlock()
					c.sendRelaysUpdate()
					return
				}

				mr.mu.Lock()
				mr.connected = true
				mr.reconnectAttempts = 0
				mr.mu.Unlock()
				c.sendRelaysUpdate()
				continue
			}

			if ev == nil {
				continue
			}
			c.processEvent(ev, mr.url)
		}
	}
}

func (c *client) processEvent(ev *nostr.Event, relayURL string) {
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
			requiredPoW := c.effectivePoWForChat(eventChat)
			if !isPoWValid(ev, requiredPoW) {
				log.Printf("Dropped event %s from %s for failing PoW check (required: %d)", safeSuffix(ev.ID, 4), eventChat, requiredPoW)
				return
			}
		}
	}

	streamKey := "chat:" + eventChat
	if av := c.getActiveView(); av != nil && av.IsGroup && slices.Contains(av.Children, eventChat) {
		streamKey = "group:" + av.Name
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
		spk = safeSuffix(ev.PubKey, 4)
	}

	c.userContext.Add(ev.PubKey, userContext{
		nick:        nick,
		chat:        eventChat,
		shortPubKey: spk,
	})

	timestamp := time.Unix(int64(ev.CreatedAt), 0).Format("15:04:05")

	isOwn := false

	if ev.PubKey == c.pk {
		isOwn = true
	} else {
		for _, s := range c.chatKeys {
			if ev.PubKey == s.PubKey {
				isOwn = true
				break
			}
		}
	}

	c.enqueueOrdered(streamKey, DisplayEvent{
		Type:         "NEW_MESSAGE",
		Timestamp:    timestamp,
		Nick:         nick,
		FullPubKey:   ev.PubKey,
		ShortPubKey:  spk,
		IsOwnMessage: isOwn,
		Content:      content,
		ID:           safeSuffix(ev.ID, 4),
		Chat:         eventChat,
		RelayURL:     relayURL,
	}, int64(ev.CreatedAt), ev.ID)
}

func (c *client) enqueueOrdered(streamKey string, de DisplayEvent, createdAt int64, id string) {
	c.orderMu.Lock()
	if len(c.orderBuf[streamKey]) >= perStreamBufferMax {
		c.orderBuf[streamKey] = c.orderBuf[streamKey][1:]
	}
	c.orderBuf[streamKey] = append(c.orderBuf[streamKey], orderItem{ev: de, createdAt: createdAt, id: id})
	if _, ok := c.orderTimers[streamKey]; !ok {
		c.orderTimers[streamKey] = time.AfterFunc(orderingFlushDelay, func() { c.flushOrdered(streamKey) })
	}
	c.orderMu.Unlock()
}

func (c *client) flushOrdered(streamKey string) {
	c.orderMu.Lock()
	buf := c.orderBuf[streamKey]
	delete(c.orderBuf, streamKey)
	delete(c.orderTimers, streamKey)
	c.orderMu.Unlock()

	if len(buf) == 0 {
		return
	}

	sort.Slice(buf, func(i, j int) bool {
		if buf[i].createdAt == buf[j].createdAt {
			return buf[i].id < buf[j].id
		}
		return buf[i].createdAt < buf[j].createdAt
	})

	for _, it := range buf {
		select {
		case c.eventsChan <- it.ev:
		case <-c.ctx.Done():
			return
		}
	}
}

func (c *client) signEventForChat(ev *nostr.Event, chatName string) error {
	view := c.getActiveView()
	useMainKey := false

	if view != nil && view.IsGroup {
		useMainKey = true
	}

	if !useMainKey {
		if session, ok := c.chatKeys[chatName]; ok && session.PrivKey != "" {
			ev.PubKey = session.PubKey
			ev.ID = ev.GetID()
			return ev.Sign(session.PrivKey)
		}
	}

	if c.sk == "" || c.pk == "" {
		return fmt.Errorf("no valid signing key available")
	}

	ev.PubKey = c.pk
	ev.ID = ev.GetID()
	return ev.Sign(c.sk)
}

func (c *client) publishMessage(message string) {
	var targetChat string
	var targetPubKey string
	if strings.HasPrefix(message, "@") {
		var matchedReplyTag string
		for _, pk := range c.userContext.Keys() {
			if ctx, ok := c.userContext.Get(pk); ok {
				replyTag := fmt.Sprintf("@%s#%s", ctx.nick, ctx.shortPubKey)
				if strings.HasPrefix(message, replyTag) {
					if len(replyTag) > len(matchedReplyTag) {
						matchedReplyTag = replyTag
						targetPubKey = pk
						targetChat = ctx.chat
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

	if geohash.Validate(targetChat) == nil {
		kind = geochatKind
		tagKey = "g"
	} else {
		kind = namedChatKind
		tagKey = "d"
	}

	relayPool := c.getRelayPoolForChat(targetChat)
	relayPoolSet := make(map[string]struct{}, len(relayPool))
	for _, url := range relayPool {
		relayPoolSet[url] = struct{}{}
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
	requiredPoW := c.effectivePoWForChat(targetChat)

	c.relaysMu.Lock()
	var relaysForPublishing []*managedRelay
	for url, r := range c.relays {
		if _, ok := relayPoolSet[url]; !ok {
			continue
		}
		if c.relayFailed(url) {
			continue
		}
		relaysForPublishing = append(relaysForPublishing, r)
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
		if err := c.signEventForChat(&ev, targetChat); err != nil {
			c.eventsChan <- DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Failed to sign event: %v", err)}
			return
		}
		c.publish(ev, targetChat, relaysForPublishing)
	}
}

func (c *client) minePoWAndPublish(ev nostr.Event, difficulty int, targetChat string, relays []*managedRelay) {
	if session, ok := c.chatKeys[targetChat]; ok && session.PrivKey != "" {
		ev.PubKey = session.PubKey
	} else {
		ev.PubKey = c.pk
	}

	c.eventsChan <- DisplayEvent{Type: "STATUS",
		Content: fmt.Sprintf("Calculating Proof-of-Work (difficulty %d)...", difficulty),
	}

	nonceTagIndex := -1
	for i, tag := range ev.Tags {
		if len(tag) > 1 && tag[0] == "nonce" {
			nonceTagIndex = i
			break
		}
	}
	if nonceTagIndex == -1 {
		c.eventsChan <- DisplayEvent{Type: "ERROR", Content: "PoW mining failed: nonce tag not found."}
		return
	}

	var nonceCounter uint64
	for {
		ev.Tags[nonceTagIndex][1] = strconv.FormatUint(nonceCounter, 10)
		ev.ID = ev.GetID()
		if countLeadingZeroBits(ev.ID) >= difficulty {
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

	if session, ok := c.chatKeys[targetChat]; ok && session.PrivKey != "" {
		_ = ev.Sign(session.PrivKey)
	} else {
		_ = ev.Sign(c.sk)
	}

	c.publish(ev, targetChat, relays)
}

func (c *client) publish(ev nostr.Event, targetChat string, relaysForPublishing []*managedRelay) {
	sort.Slice(relaysForPublishing, func(i, j int) bool {
		return relaysForPublishing[i].latency < relaysForPublishing[j].latency
	})

	var wg sync.WaitGroup
	successCount := 0
	var errorMessages []string
	var mu sync.Mutex

	for _, r := range relaysForPublishing {
		wg.Add(1)
		go func(r *managedRelay) {
			defer wg.Done()
			if err := r.relay.Publish(c.ctx, ev); err == nil {
				mu.Lock()
				successCount++
				mu.Unlock()
			} else {
				mu.Lock()
				errorMessages = append(errorMessages, fmt.Sprintf("%s: %v", r.url, err))
				mu.Unlock()

				r.mu.Lock()
				r.connected = false
				r.mu.Unlock()

				c.markRelayFailed(r.url)

				c.sendRelaysUpdate()
			}
		}(r)
	}
	wg.Wait()

	c.eventsChan <- DisplayEvent{
		Type: "STATUS",
		Content: fmt.Sprintf("Event %s sent to %d/%d relays for %s.",
			safeSuffix(ev.ID, 4), successCount, len(relaysForPublishing), targetChat),
	}

	for _, em := range errorMessages {
		c.eventsChan <- DisplayEvent{
			Type: "ERROR", Content: "Publish failed on " + em,
		}
		if pow, ok := parsePowHint(em); ok && pow > 0 {
			c.eventsChan <- DisplayEvent{
				Type:    "INFO",
				Content: fmt.Sprintf("Hint: relay suggests PoW %d for %s. Try `/pow %d` and resend.", pow, targetChat, pow),
			}
		}
	}
}

func (c *client) createEvent(message string, kind int, tags nostr.Tags, difficulty int) nostr.Event {
	baseTags := make(nostr.Tags, 0, len(tags)+2)
	baseTags = append(baseTags, tags...)

	active := c.getActiveView()
	if active != nil && !active.IsGroup {
		if session, ok := c.chatKeys[active.Name]; ok && session.Nick != "" {
			baseTags = append(baseTags, nostr.Tag{"n", session.Nick})
		}
	} else if active != nil && active.IsGroup {
		nick := c.config.Nick
		if nick == "" {
			nick = npubToTokiPona(c.pk)
		}
		baseTags = append(baseTags, nostr.Tag{"n", nick})
	}

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

func (c *client) sendRelaysUpdate() {
	c.relaysMu.Lock()
	defer c.relaysMu.Unlock()

	statuses := make([]RelayInfo, 0, len(c.relays))
	for _, mr := range c.relays {
		mr.mu.Lock()
		connected := mr.connected
		latency := mr.latency
		mr.mu.Unlock()

		statuses = append(statuses, RelayInfo{
			URL:       mr.url,
			Latency:   latency,
			Connected: connected,
		})
	}

	c.eventsChan <- DisplayEvent{Type: "RELAYS_UPDATE", Payload: statuses}
}

// --- Helpers ---

func retryWithBackoff(ctx context.Context, fn func() error, attempt int) error {
	delay := min(time.Duration(math.Pow(2, float64(attempt-1)))*500*time.Millisecond, 30*time.Second)

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(delay):
		if err := fn(); err != nil {
			return err
		}
		return nil
	}
}

func (c *client) effectivePoWForChat(chat string) int {
	for _, v := range c.config.Views {
		if !v.IsGroup && v.Name == chat && v.PoW > 0 {
			return v.PoW
		}
	}
	if av := c.getActiveView(); av != nil && av.IsGroup && av.PoW > 0 {
		for _, child := range av.Children {
			if child == chat {
				return av.PoW
			}
		}
	}
	return 0
}
