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
	// Safety ceiling for a whole active group, not a per-chat target.
	maxActiveRelays         = 256
	maxConcurrentRelayDials = 4
	relayRetryCacheSize     = 4096
	connectTimeout          = 5 * time.Second
	maxFrameBytes           = 64 << 10
)

type relaySubscription struct {
	chats []string
	since nostr.Timestamp
}

// The client loop owns these handles; the worker owns its socket and subscription.
type managedRelay struct {
	url         string
	cancel      context.CancelFunc
	done        chan struct{}
	chats       []string
	updates     chan relaySubscription
	publish     chan relayPublish
	connected   bool
	latency     time.Duration
	connectedAt time.Time
	retry       relayRetry      // immutable retry snapshot passed to the worker
	startSince  nostr.Timestamp // immutable initial subscription watermark
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
	retry     relayRetry
}

func (c *client) getRelayPoolForChat(chat string) []string {
	urls := slices.Clone(c.config.AnchorRelays)
	if geohash.Validate(chat) == nil {
		// Nearest relays of both bundled catalogs (BitChat iOS and upstream
		// georelays), because the two variants target different nearest sets.
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
			worker.updates <- c.subscriptionForRelay(url, chats)
		} else if len(c.relays) < maxActiveRelays {
			ctx, cancel := context.WithCancel(c.ctx)
			worker := &managedRelay{url: url, cancel: cancel, done: make(chan struct{}), chats: chats, updates: make(chan relaySubscription, 1), publish: make(chan relayPublish, 1)}
			worker.retry, _ = c.relayRetries.Peek(url)
			worker.startSince = c.subscriptionForRelay(url, chats).since
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

// Bound both stalled handshakes and fast refusals. Waiting and dialing are
// cancelable; a connected socket does not retain a dial permit.
func (c *client) dialRelay(ctx context.Context, url string) (*websocket.Conn, time.Duration, error) {
	select {
	case c.dialSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	}
	defer func() { <-c.dialSlots }()
	select {
	case c.dialPace <- struct{}{}:
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	}
	timer := time.NewTimer(max(0, time.Until(c.nextDial)))
	select {
	case <-timer.C:
		c.nextDial = time.Now().Add(c.relayTiming.dialGap)
		<-c.dialPace
	case <-ctx.Done():
		timer.Stop()
		<-c.dialPace
		return nil, 0, ctx.Err()
	}
	timeout := connectTimeout
	var options *websocket.DialOptions
	if c.relayHTTPClient != nil {
		timeout = socksConnectTimeout
		options = &websocket.DialOptions{HTTPClient: c.relayHTTPClient}
	}
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	conn, _, err := websocket.Dial(dialCtx, url, options)
	return conn, time.Since(start), err
}

// Retry forever while selected. Neither an initial failure nor CLOSED permanently
// blacklists a relay. Backoff resets only after a stable connection, not a handshake.
func (c *client) runRelay(ctx context.Context, worker *managedRelay, chats []string) {
	delay := max(c.relayTiming.minimum, worker.retry.delay)
	since := worker.startSince
	if since == 0 {
		since = nostr.Now()
	}
	if worker.retry.since > 0 && slices.Equal(worker.retry.chats, chats) {
		since = min(since, max(worker.retry.since, nostr.Now()-30))
	}
	for ctx.Err() == nil {
		conn, latency, err := c.dialRelay(ctx, worker.url)
		if err == nil {
			conn.SetReadLimit(maxFrameBytes)
			connectedAt := time.Now()
			chats = c.serveRelay(ctx, worker, conn, chats, since, latency)
			conn.CloseNow()
			if time.Since(connectedAt) >= c.relayTiming.stable {
				delay = c.relayTiming.minimum
			}
		}
		if ctx.Err() != nil {
			return
		}
		// Only a small overlap is requested; ephemeral relays need not retain it.
		since = max(since, nostr.Now()-30)
		pause := delay/2 + time.Duration(rand.Int64N(int64(delay/2)+1))
		delay = min(delay*2, c.relayTiming.maximum)
		retry := relayRetry{until: time.Now().Add(pause), delay: delay, since: since, chats: slices.Clone(chats)}
		select {
		case c.incoming <- relayEvent{worker: worker, retry: retry}:
		case <-ctx.Done():
			return
		}
		timer := time.NewTimer(time.Until(retry.until))
	wait:
		for {
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case update := <-worker.updates:
				chats, since = update.chats, update.since
			case request := <-worker.publish:
				request.result <- errors.New("relay is reconnecting")
			case <-timer.C:
				break wait
			}
		}
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
	// Admission or owner backpressure may have aged the snapshot before use.
	since = max(since, nostr.Now()-30)
	frame := []any{"REQ", "chat"}
	for _, filter := range chatFilters(chats, since) {
		frame = append(frame, filter)
	}
	return writeFrame(ctx, conn, frame)
}

// One reader, one bounded queue, no goroutine per frame. The websocket library
// handles control frames while Read is active; Ping has a bounded pong deadline.
func (c *client) serveRelay(ctx context.Context, worker *managedRelay, conn *websocket.Conn, chats []string, since nostr.Timestamp, latency time.Duration) []string {
	ctx, cancel := context.WithCancel(ctx)
	// Keep aggregate buffering small when a group uses many relay sockets.
	frames := make(chan []byte, 4)
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
	c.relayStatus(ctx, worker, true, latency)
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
		case update := <-worker.updates:
			chats, since = update.chats, update.since
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
