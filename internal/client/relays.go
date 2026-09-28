package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/coder/websocket"
	"github.com/mmcloughlin/geohash"
	"github.com/nbd-wtf/go-nostr"
)

const (
	maxActiveRelays = 12
	connectTimeout  = 5 * time.Second
	maxFrameBytes   = 64 << 10
)

// The client loop owns these handles; the worker owns its socket and subscription.
type managedRelay struct {
	url       string
	cancel    context.CancelFunc
	done      chan struct{}
	chats     []string
	updates   chan []string
	publish   chan relayPublish
	connected bool
	latency   time.Duration
}

type relayPublish struct {
	ctx    context.Context
	event  nostr.Event
	result chan error
}

type relayEvent struct {
	worker    *managedRelay
	event     *nostr.Event
	connected bool
	latency   time.Duration
}

func (c *client) getRelayPoolForChat(chat string) []string {
	urls := slices.Clone(c.config.AnchorRelays)
	if geohash.Validate(chat) == nil {
		// Nearest relays of both bundled catalogs (BitChat iOS and upstream
		// georelays), because the two variants target different nearest sets.
		if closest, err := closestRelays(chat, defaultRelayCount); err == nil {
			urls = append(urls, closest...)
		}
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

func (c *client) updateAllSubscriptions() {
	var chats []string
	if view := c.getActiveView(); view != nil {
		if view.IsGroup {
			chats = slices.Clone(view.Children)
		} else {
			chats = []string{view.Name}
		}
	}
	slices.Sort(chats)
	desired := make(map[string][]string)
	for _, chat := range chats {
		for _, url := range c.getRelayPoolForChat(chat) {
			if _, exists := desired[url]; !exists && len(desired) >= maxActiveRelays {
				continue
			}
			desired[url] = append(desired[url], chat)
		}
	}
	c.updateRelaySubscriptions(desired)
}

func (c *client) updateRelaySubscriptions(desired map[string][]string) {
	var retired []*managedRelay
	for url, worker := range c.relays {
		if _, needed := desired[url]; !needed {
			worker.cancel()
			retired = append(retired, worker)
			delete(c.relays, url)
		}
	}
	// Cancel all first, then join before starting replacements. The relay cap
	// bounds live workers even when views change faster than sockets shut down.
	for _, worker := range retired {
		if worker.done != nil {
			<-worker.done
		}
	}
	urls := make([]string, 0, len(desired))
	for url := range desired {
		urls = append(urls, url)
	}
	slices.Sort(urls)
	for _, url := range urls {
		chats := slices.Clone(desired[url])
		slices.Sort(chats)
		chats = slices.Compact(chats)
		if len(chats) == 0 {
			continue
		}
		if worker := c.relays[url]; worker != nil {
			if slices.Equal(worker.chats, chats) {
				continue
			}
			worker.chats = chats
			select {
			case <-worker.updates:
			default:
			}
			worker.updates <- chats
		} else if len(c.relays) < maxActiveRelays {
			ctx, cancel := context.WithCancel(c.ctx)
			worker := &managedRelay{url: url, cancel: cancel, done: make(chan struct{}), chats: chats, updates: make(chan []string, 1), publish: make(chan relayPublish, 1)}
			c.relays[url] = worker
			c.wg.Go(func() { defer close(worker.done); c.runRelay(ctx, worker, chats) })
		}
	}
	c.sendRelaysUpdate()
}

func chatFilters(chats []string, since nostr.Timestamp) nostr.Filters {
	var geo, topics []string
	for _, chat := range chats {
		if geohash.Validate(chat) == nil {
			geo = append(geo, chat)
		} else {
			topics = append(topics, chat)
		}
	}
	var filters nostr.Filters
	if len(geo) > 0 {
		filters = append(filters, nostr.Filter{Kinds: []int{geoChatKind}, Tags: nostr.TagMap{"g": geo}, Since: &since, Limit: 200})
	}
	if len(topics) > 0 {
		filters = append(filters, nostr.Filter{Kinds: []int{ephChatKind}, Tags: nostr.TagMap{"d": topics}, Since: &since, Limit: 200})
	}
	return filters
}

func (c *client) relayStatus(ctx context.Context, worker *managedRelay, connected bool, latency time.Duration) {
	select {
	case c.incoming <- relayEvent{worker: worker, connected: connected, latency: latency}:
	case <-ctx.Done():
	}
}

// Retry forever while selected. Neither an initial failure nor CLOSED permanently
// blacklists a relay. Backoff resets only after a stable connection, not a handshake.
func (c *client) runRelay(ctx context.Context, worker *managedRelay, chats []string) {
	delay := 500 * time.Millisecond
	since := nostr.Now()
	for ctx.Err() == nil {
		start := time.Now()
		dialCtx, cancel := context.WithTimeout(ctx, connectTimeout)
		conn, _, err := websocket.Dial(dialCtx, worker.url, nil)
		cancel()
		if err == nil {
			conn.SetReadLimit(maxFrameBytes)
			c.relayStatus(ctx, worker, true, time.Since(start))
			chats = c.serveRelay(ctx, worker, conn, chats, since)
			conn.CloseNow()
			c.relayStatus(ctx, worker, false, 0)
			if time.Since(start) > time.Minute {
				delay = 500 * time.Millisecond
			}
		}
		// Only a small overlap is requested; ephemeral relays need not retain it.
		since = max(since, nostr.Now()-30)
		timer := time.NewTimer(delay/2 + time.Duration(rand.Int64N(int64(delay/2)+1)))
	wait:
		for {
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case chats = <-worker.updates:
				since = nostr.Now()
			case request := <-worker.publish:
				request.result <- errors.New("relay is reconnecting")
			case <-timer.C:
				break wait
			}
		}
		delay = min(delay*2, 30*time.Second)
	}
}

func writeFrame(ctx context.Context, conn *websocket.Conn, frame any) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	return conn.Write(ctx, websocket.MessageText, data)
}

func subscribe(ctx context.Context, conn *websocket.Conn, chats []string, since nostr.Timestamp) error {
	frame := []any{"REQ", "chat"}
	for _, filter := range chatFilters(chats, since) {
		frame = append(frame, filter)
	}
	return writeFrame(ctx, conn, frame)
}

// One reader, one bounded queue, no goroutine per frame. The websocket library
// handles control frames while Read is active; Ping has a bounded pong deadline.
func (c *client) serveRelay(ctx context.Context, worker *managedRelay, conn *websocket.Conn, chats []string, since nostr.Timestamp) []string {
	ctx, cancel := context.WithCancel(ctx)
	frames := make(chan []byte, 16)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer close(frames)
		for {
			kind, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if kind != websocket.MessageText {
				continue
			}
			select {
			case frames <- data:
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() { cancel(); conn.CloseNow(); <-readerDone }()
	var pending *relayPublish
	var pendingDone <-chan struct{}
	defer func() {
		if pending != nil {
			pending.result <- errors.New("relay disconnected before acknowledgement")
		}
	}()
	if subscribe(ctx, conn, chats, since) != nil {
		return chats
	}
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return chats
		case <-pendingDone:
			pending.result <- pending.ctx.Err()
			pending = nil
			pendingDone = nil
		case <-ping.C:
			pingCtx, stop := context.WithTimeout(ctx, connectTimeout)
			err := conn.Ping(pingCtx)
			stop()
			if err != nil {
				return chats
			}
		case chats = <-worker.updates:
			since = nostr.Now()
			if subscribe(ctx, conn, chats, since) != nil {
				return chats
			}
		case request := <-worker.publish:
			if request.ctx.Err() != nil {
				request.result <- request.ctx.Err()
				continue
			}
			if pending != nil {
				request.result <- errors.New("relay publish queue is busy")
				continue
			}
			if err := writeFrame(request.ctx, conn, []any{"EVENT", request.event}); err != nil {
				request.result <- err
				return chats
			}
			pending = &request
			pendingDone = request.ctx.Done()
		case data, ok := <-frames:
			if !ok {
				return chats
			}
			var frame []json.RawMessage
			if json.Unmarshal(data, &frame) != nil || len(frame) < 2 {
				continue
			}
			var label, id string
			if json.Unmarshal(frame[0], &label) != nil || json.Unmarshal(frame[1], &id) != nil {
				continue
			}
			switch label {
			case "EVENT":
				if id != "chat" || len(frame) != 3 {
					continue
				}
				var event nostr.Event
				if json.Unmarshal(frame[2], &event) != nil {
					continue
				}
				select {
				case c.incoming <- relayEvent{worker: worker, event: &event}:
				case <-ctx.Done():
					return chats
				}
			case "CLOSED":
				if id == "chat" {
					return chats
				}
			case "OK":
				if len(frame) != 4 || pending == nil || id != pending.event.ID {
					continue
				}
				var accepted bool
				var reason string
				if json.Unmarshal(frame[2], &accepted) != nil || json.Unmarshal(frame[3], &reason) != nil {
					continue
				}
				var err error
				if !accepted {
					err = fmt.Errorf("relay rejected event: %s", truncateString(sanitizeString(reason), 200))
				}
				pending.result <- err
				pending = nil
				pendingDone = nil
			}
		}
	}
}

func (c *client) sendRelaysUpdate() {
	statuses := make([]RelayInfo, 0, len(c.relays))
	for _, worker := range c.relays {
		statuses = append(statuses, RelayInfo{URL: worker.url, Connected: worker.connected, Latency: worker.latency})
	}
	slices.SortFunc(statuses, func(a, b RelayInfo) int {
		if a.URL < b.URL {
			return -1
		}
		if a.URL > b.URL {
			return 1
		}
		return 0
	})
	c.emit(DisplayEvent{Type: "RELAYS_UPDATE", Payload: statuses})
}
