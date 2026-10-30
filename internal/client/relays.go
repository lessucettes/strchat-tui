package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"fiatjaf.com/nostr"
)

const (
	// Safety ceiling for a whole active group, not a per-chat target.
	maxActiveRelays         = 256
	maxConcurrentRelayDials = 4
	relayRetryCacheSize     = 4096
	connectTimeout          = 5 * time.Second
	maxFrameBytes           = 64 << 10
	// relayPingInterval is the idle probe cadence; relayPongWait bounds one
	// probe. Both are production defaults; serveRelayWithPing takes them as
	// parameters so tests can drive probes without sleeping.
	relayPingInterval = 30 * time.Second
	relayPongWait     = connectTimeout
)

type relaySubscription struct {
	chats     []string
	since     map[string]nostr.Timestamp // immutable per-chat recovery watermarks
	activated map[string]nostr.Timestamp // immutable start of each chat's active interval
	keys      map[string]string          // immutable public-key -> signing-key snapshot
}

// Resume only continuously active chats. Coalesced updates and retry snapshots
// can still name a chat that was left and rejoined; its old cursor must not win.
// Every recovery remains bounded even if a snapshot waited in a queue.
func (sub relaySubscription) resume(previous relaySubscription) relaySubscription {
	now := nostr.Now()
	watermarks := make(map[string]nostr.Timestamp, len(sub.chats))
	for _, chat := range sub.chats {
		since := sub.since[chat]
		if since == 0 {
			since = now
		}
		if saved := previous.since[chat]; saved > 0 && sub.activated[chat] == previous.activated[chat] {
			since = min(since, saved)
		}
		watermarks[chat] = max(since, now-30)
	}
	sub.since = watermarks
	return sub
}

// The client loop owns these handles; the worker owns its socket and subscription.
type managedRelay struct {
	url         string
	cancel      context.CancelFunc
	done        chan struct{}
	chats       []string
	keys        map[string]string // owner-only current subscription identities
	updates     chan relaySubscription
	publish     chan relayPublish
	heartbeat   chan relayHeartbeat // best-effort, separate from user publishes
	connected   bool
	latency     time.Duration
	connectedAt time.Time
	retry       relayRetry // immutable retry snapshot passed to the worker
	lastProblem string     // owner-only dedup of relay-side failure warnings

}

type relayPublish struct {
	ctx    context.Context
	event  nostr.Event
	key    string // immutable signing identity of this queued event
	result chan error
}

type relayEvent struct {
	worker    *managedRelay
	event     *nostr.Event
	connected bool
	latency   time.Duration
	retry     relayRetry
	problem   string
}

// relayProbe carries one asynchronous ping's outcome together with the reader
// revision observed when that probe started.
type relayProbe struct {
	err      error
	revision uint64
}

// getRelayPoolForChat returns a chat's effective relay pool: configured anchors
// plus, for geohash chats, the merged nearest sets of both bundled catalogs.
// The result is the immutable owner-owned plan slice and must not be modified
// by callers. Normalization and geo ranking happen once per anchors revision
// rather than once per relay status, which used to dominate large-group
// refreshes.
func (c *client) getRelayPoolForChat(chat string) []string {
	c.syncRelayPlanning()
	return c.relayPlanFor(chat).pool
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
			// Compare the key/chat topology before constructing a subscription.
			// Connection and latency statuses dominate refreshes and normally
			// leave both unchanged; only a real topology or identity change
			// needs a new snapshot and worker update.
			if slices.Equal(worker.chats, chats) && c.relayKeysMatch(worker.keys, chats) {
				continue
			}
			subscription := c.subscriptionForRelay(url, chats)
			worker.chats = chats
			worker.keys = subscription.keys
			select {
			case <-worker.updates:
			default:
			}
			worker.updates <- subscription
		} else if len(c.relays) < maxActiveRelays {
			subscription := c.subscriptionForRelay(url, chats)
			ctx, cancel := context.WithCancel(c.ctx)
			worker := &managedRelay{url: url, cancel: cancel, done: make(chan struct{}), chats: chats, keys: subscription.keys, updates: make(chan relaySubscription, 1), publish: make(chan relayPublish, 1), heartbeat: make(chan relayHeartbeat, 1)}
			worker.retry, _ = c.relayRetries.Peek(url)
			worker.lastProblem = worker.retry.lastProblem
			c.relays[url] = worker
			c.wg.Go(func() { defer close(worker.done); c.runRelay(ctx, worker, subscription) })
		}
	}
	// Workers must start before a display write can block, but report the route
	// before any connection status. Consume the notice so refreshes stay silent.
	if c.proxyStartupNotice != "" {
		c.emit(DisplayEvent{Type: "STATUS", Content: c.proxyStartupNotice})
		c.proxyStartupNotice = ""
	}
	c.sendRelaysUpdate()
}

func chatFilters(chats []string, since nostr.Timestamp) []nostr.Filter {
	var geo, topics []string
	for _, chat := range chats {
		if isGeohashName(chat) {
			geo = append(geo, chat)
		} else {
			topics = append(topics, chat)
		}
	}
	var filters []nostr.Filter
	if len(geo) > 0 {
		filters = append(filters, nostr.Filter{Kinds: []nostr.Kind{geoChatKind}, Tags: nostr.TagMap{"g": geo}, Since: since, Limit: 200})
		filters = append(filters, nostr.Filter{Kinds: []nostr.Kind{presenceKind}, Tags: nostr.TagMap{"g": geo}, Since: nostr.Now() - 120, Limit: 200})
	}
	if len(topics) > 0 {
		filters = append(filters, nostr.Filter{Kinds: []nostr.Kind{ephChatKind}, Tags: nostr.TagMap{"d": topics}, Since: since, Limit: 200})
		filters = append(filters, nostr.Filter{Kinds: []nostr.Kind{presenceKind}, Tags: nostr.TagMap{"d": topics}, Since: nostr.Now() - 120, Limit: 200})
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
func (c *client) dialRelay(ctx context.Context, url string) (relayConn, time.Duration, error) {
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
	if c.relayHTTPClient != nil {
		timeout = socksConnectTimeout
	}
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	conn, err := dialRelayTransport(dialCtx, url, c.relayHTTPClient, timeout)
	return conn, time.Since(start), err
}

// Retry forever while selected. Neither an initial failure nor CLOSED permanently
// blacklists a relay. Backoff resets only after a stable connection, not a handshake.
func (c *client) runRelay(ctx context.Context, worker *managedRelay, subscription relaySubscription) {
	delay := max(c.relayTiming.minimum, worker.retry.delay)
	subscription = subscription.resume(relaySubscription{since: worker.retry.since, activated: worker.retry.activated})
	for ctx.Err() == nil {
		conn, latency, err := c.dialRelay(ctx, worker.url)
		if err == nil {
			conn.SetReadLimit(maxFrameBytes)
			connectedAt := time.Now()
			var authFailed bool
			subscription, authFailed, err = c.serveRelay(ctx, worker, conn, subscription, latency)
			conn.CloseNow()
			if authFailed {
				// A fresh random key cannot fix a permission denial. Keep normal
				// recovery, but do not hammer an auth/restricted relay on reconnect.
				delay = max(delay, time.Minute)
			} else if time.Since(connectedAt) >= c.relayTiming.stable {
				delay = c.relayTiming.minimum
			}
		}
		if ctx.Err() != nil {
			return
		}
		// Only a small overlap is requested; ephemeral relays need not retain it.
		subscription = subscription.resume(relaySubscription{})
		pause := delay/2 + time.Duration(rand.Int64N(int64(delay/2)+1))
		delay = min(delay*2, c.relayTiming.maximum)
		retry := relayRetry{until: time.Now().Add(pause), delay: delay, since: subscription.since, activated: subscription.activated}
		if err != nil {
			retry.failure = relayProblemText(err)
		}
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
				subscription = update.resume(subscription)
			case request := <-worker.publish:
				request.result <- errors.New("relay is reconnecting")
			case <-timer.C:
				break wait
			}
		}
	}
}

func writeFrame(ctx context.Context, conn relayConn, frame any) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	return conn.Write(ctx, relayMessageText, data)
}

func subscribe(ctx context.Context, conn relayConn, sub relaySubscription) error {
	// Admission or owner backpressure may have aged the snapshot before use.
	sub = sub.resume(relaySubscription{})
	groups := make(map[nostr.Timestamp][]string)
	var watermarks []nostr.Timestamp
	for _, chat := range sub.chats {
		since := sub.since[chat]
		if _, exists := groups[since]; !exists {
			watermarks = append(watermarks, since)
		}
		groups[since] = append(groups[since], chat)
	}
	slices.Sort(watermarks)
	frame := []any{"REQ", "chat"}
	for _, since := range watermarks {
		for _, filter := range chatFilters(groups[since], since) {
			frame = append(frame, filter)
		}
	}
	return writeFrame(ctx, conn, frame)
}

type relayReadFrame struct {
	parts     [relayEnvelopeParts][]byte
	count     int
	label, id string
	event     *nostr.Event
}

// One reader, bounded queues, no goroutine per frame. Heartbeats bypass the
// ordinary frame queue as well as incoming; a busy activity worker cannot
// prevent the reader from reaching a chat message or a WebSocket control frame.
func (c *client) serveRelay(ctx context.Context, worker *managedRelay, conn relayConn, sub relaySubscription, latency time.Duration) (relaySubscription, bool, error) {
	ticker := time.NewTicker(relayPingInterval)
	defer ticker.Stop()
	return c.serveRelayWithPing(ctx, worker, conn, sub, latency, ticker.C, relayPongWait)
}

// serveRelayWithPing serves one connection until it fails or the context ends.
// ticks is the idle-probe cadence and pongWait bounds a single probe; the
// production wrapper supplies relayPingInterval and relayPongWait. A probe runs
// asynchronously so the serve loop keeps draining the reader even while a pong
// is outstanding; a missed pong is only fatal when the reader itself is idle,
// because otherwise the pong is queued behind data we have not drained yet.
func (c *client) serveRelayWithPing(ctx context.Context, worker *managedRelay, conn relayConn, sub relaySubscription, latency time.Duration, ticks <-chan time.Time, pongWait time.Duration) (relaySubscription, bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	// Bind lifetime cancellation once for the whole connection. Closing the
	// socket unblocks the reader and any stalled writer; no per-frame context
	// scaffold or gorilla deadline-field mutation is needed.
	stopLifetime := context.AfterFunc(ctx, func() { _ = conn.CloseNow() })
	defer cancel()
	defer stopLifetime()
	// Keep aggregate buffering small when a group uses many relay sockets.
	frames := make(chan relayReadFrame, 4)
	readerDone := make(chan struct{})
	// The reader publishes cheap liveness evidence for the serve loop: a
	// revision advanced after every successful read, and a flag set exactly
	// while it is parked handing us a frame. A missed pong is inconclusive
	// whenever either one shows the connection is busy, because the pong is
	// then queued behind data we have not drained yet.
	var readRevision atomic.Uint64
	var readerBlocked atomic.Bool
	var readErr error // Published before the reader closes frames.
	go func() {
		defer close(readerDone)
		defer close(frames)
		for {
			kind, data, err := conn.Read(ctx)
			if err != nil {
				readErr = err
				return
			}
			readRevision.Add(1)
			if kind != relayMessageText {
				continue
			}
			parts, count := splitRelayEnvelope(data)
			if count < 2 {
				continue
			}
			label, id, validHead := decodeRelayEnvelopeHead(parts[0], parts[1])
			if !validHead {
				continue
			}
			frame := relayReadFrame{parts: parts, count: count, label: label, id: id}
			if label == "EVENT" {
				if id != "chat" || count != 3 {
					continue
				}
				event, err := decodeRelayEvent(parts[2])
				if err != nil {
					continue
				}
				if event.Kind == presenceKind {
					c.activity.heartbeat(event)
					continue
				}
				frame.event = event
				frame.parts = [relayEnvelopeParts][]byte{} // do not retain the raw EVENT too
			}
			select {
			case frames <- frame:
				continue
			default:
			}
			readerBlocked.Store(true)
			select {
			case frames <- frame:
				readerBlocked.Store(false)
			case <-ctx.Done():
				readerBlocked.Store(false)
				return
			}
		}
	}()
	var pinging bool
	var lastActivity uint64
	probeDone := make(chan relayProbe, 1)
	defer func() {
		cancel()
		conn.CloseNow()
		<-readerDone
		// A probe waits for a pong or the connection lifetime, never for
		// shutdown: closing the socket and cancelling ctx end it promptly.
		if pinging {
			<-probeDone
		}
	}()
	var pending *relayPublish
	var pendingDone <-chan struct{}
	var pendingWait, pendingSent, publishRetried bool
	var terminalErr error
	defer func() {
		if pending != nil {
			if terminalErr == nil {
				terminalErr = errors.New("relay disconnected before acknowledgement")
			}
			pending.result <- terminalErr
		}
	}()
	if err := subscribe(ctx, conn, sub); err != nil {
		return sub, false, err
	}
	c.relayStatus(ctx, worker, true, latency)
	var auth relayAuth
	var subWait, subRetried, authReported bool
	reportedProblem := ""
	authTimer := time.NewTimer(time.Hour)
	authTimer.Stop()
	defer authTimer.Stop()
	var authC <-chan time.Time
	var authDeadline time.Time
	// A queued event may outlive its originating view. Authenticate that exact
	// accepted job's key too; never look up a replacement identity on the worker.
	authKeys := func() map[string]string {
		keys := maps.Clone(sub.keys)
		if keys == nil {
			keys = make(map[string]string)
		}
		if pending != nil && pending.key != "" {
			keys[pending.event.PubKey.Hex()] = pending.key
		}
		return keys
	}
	startAuth := func() error {
		keys := authKeys()
		if len(keys) == 0 {
			return errors.New("no chat identity for relay authentication")
		}
		if authC == nil {
			authDeadline = time.Now().Add(connectTimeout)
			authTimer.Reset(time.Until(authDeadline))
			authC = authTimer.C
		}
		authCtx, stop := context.WithDeadline(ctx, authDeadline)
		defer stop()
		return auth.send(authCtx, conn, worker.url, keys)
	}
	finishPending := func(err error) {
		pending.result <- err
		pending, pendingDone = nil, nil
		pendingWait, pendingSent, publishRetried = false, false, false
		auth.prune(sub.keys)
	}
	// A rejection can be observed both by the OK handler and by failAuth when
	// resume observes the same attempt. Report each distinct failure once per
	// connection; the owner deduplicates repeats across cooldown reconnects.
	reportProblem := func(err error) {
		text := relayProblemText(err)
		if text == reportedProblem {
			return
		}
		reportedProblem = text
		c.relayProblem(ctx, worker, err)
	}
	failAuth := func(err error) (relaySubscription, bool, error) {
		terminalErr = err
		reportProblem(err)
		return sub, true, err
	}
	// Only explicit rejections are retried, once for this challenge/request.
	// An AUTH OK is not an acknowledgement of the pending chat EVENT.
	resume := func() error {
		if pendingWait {
			if err := pending.ctx.Err(); err != nil {
				finishPending(err)
			} else if attempt := auth.attempts[pending.event.PubKey.Hex()]; attempt != nil && attempt.done {
				if attempt.err != nil {
					finishPending(attempt.err)
				} else {
					if err := writeFrame(pending.ctx, conn, []any{"EVENT", pending.event}); err != nil {
						return err
					}
					pendingWait = false
					publishRetried = pendingSent
					pendingSent = true
				}
			}
		}
		if subWait && auth.complete(sub.keys) {
			var accepted bool
			var rejected error
			for pubkey := range sub.keys {
				attempt := auth.attempts[pubkey]
				if attempt.err == nil {
					accepted = true
				} else {
					rejected = attempt.err
				}
			}
			if !accepted {
				if rejected == nil {
					rejected = errors.New("no authenticated chat identity")
				}
				return rejected
			}
			if err := subscribe(ctx, conn, sub); err != nil {
				return err
			}
			subWait, subRetried = false, true
			c.relayStatus(ctx, worker, true, latency)
		}
		if !subWait && !pendingWait && auth.complete(authKeys()) {
			authTimer.Stop()
			authC = nil
		}
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return sub, false, ctx.Err()
		case <-authC:
			return failAuth(errors.New("relay authentication timed out waiting for a challenge or AUTH acknowledgement"))
		case <-pendingDone:
			finishPending(pending.ctx.Err())
		case <-ticks:
			revision := readRevision.Load()
			// Frames already arriving, queued frames, or a reader parked on
			// our full queue prove liveness better than an idle control probe.
			if !pinging && revision == lastActivity && len(frames) == 0 && !readerBlocked.Load() && ctx.Err() == nil {
				pinging = true
				go func(revision uint64) {
					pingCtx, stop := context.WithTimeout(ctx, pongWait)
					err := conn.Ping(pingCtx)
					stop()
					probeDone <- relayProbe{err: err, revision: revision}
				}(revision)
			}
			lastActivity = revision
		case probe := <-probeDone:
			pinging = false
			// A missed pong only proves the peer is gone when the reader had
			// no traffic and was not parked on our own queue. Otherwise the
			// exact pong is behind frames we have not drained, and treating
			// the timeout as remote death would be self-inflicted.
			if probe.err != nil && ctx.Err() == nil && readRevision.Load() == probe.revision && len(frames) == 0 && !readerBlocked.Load() {
				return sub, false, probe.err
			}
		case update := <-worker.updates:
			sub = update.resume(sub)
			auth.prune(authKeys())
			subRetried = false
			if auth.challenged {
				subWait = true
				if err := startAuth(); err != nil {
					return failAuth(err)
				}
			} else if err := subscribe(ctx, conn, sub); err != nil {
				return sub, false, err
			}
		case heartbeat := <-worker.heartbeat:
			// Never wait for an ephemeral EVENT acknowledgement. A pending user
			// send, queued message or AUTH exchange wins; stale work is dropped.
			if heartbeat.ctx.Err() != nil || heartbeat.event.CreatedAt < nostr.Now()-80 || pending != nil || len(worker.publish) > 0 || subWait ||
				sub.keys[heartbeat.event.PubKey.Hex()] == "" ||
				(auth.challenged && !auth.complete(sub.keys)) {
				continue
			}
			// Once a socket write starts it belongs to the connection lifetime:
			// interrupting it on a view change can poison the WebSocket writer.
			if err := writeFrame(ctx, conn, []any{"EVENT", heartbeat.event}); err != nil {
				return sub, false, err
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
			pending = &request
			pendingDone = request.ctx.Done()
			if auth.challenged {
				pendingWait = true
				if err := startAuth(); err != nil {
					return failAuth(err)
				}
			} else if err := writeFrame(request.ctx, conn, []any{"EVENT", request.event}); err != nil {
				finishPending(err)
				return sub, false, err
			} else {
				pendingSent = true
			}
		case frame, ok := <-frames:
			if !ok {
				return sub, false, readErr
			}
			parts, count, label, id := frame.parts, frame.count, frame.label, frame.id
			switch label {
			case "AUTH":
				if count != 2 {
					continue
				}
				changed, err := auth.setChallenge(id)
				if err != nil {
					return failAuth(err)
				}
				if !changed {
					continue
				}
				subRetried, authReported, reportedProblem = false, false, ""
				if err := startAuth(); err != nil {
					return failAuth(fmt.Errorf("relay authentication failed: %w", err))
				}
			case "EVENT":
				select {
				case c.incoming <- relayEvent{worker: worker, event: frame.event}:
				case <-ctx.Done():
					return sub, false, ctx.Err()
				}
			case "EOSE":
				var hints []string
				if id == "chat" && count == 3 && !subRetried && json.Unmarshal(parts[2], &hints) == nil && slices.Contains(hints, "auth") {
					subWait = true
					if err := startAuth(); err != nil {
						return failAuth(err)
					}
				}
			case "CLOSED":
				var reason string
				if id != "chat" || count != 3 || json.Unmarshal(parts[2], &reason) != nil {
					continue
				}
				if strings.HasPrefix(reason, "auth-required:") {
					if subRetried {
						return failAuth(relayRejection("relay still requires authentication after AUTH", reason))
					}
					subWait = true
					c.relayStatus(ctx, worker, false, 0)
					if err := startAuth(); err != nil {
						return failAuth(err)
					}
				} else if strings.HasPrefix(reason, "restricted:") {
					return failAuth(relayRejection("relay access restricted", reason))
				} else {
					return sub, false, relayRejection("relay closed the chat subscription", reason)
				}
			case "OK":
				if count != 4 {
					continue
				}
				var accepted bool
				var reason string
				if json.Unmarshal(parts[2], &accepted) != nil || json.Unmarshal(parts[3], &reason) != nil {
					continue
				}
				if pubkey, matched := auth.acknowledge(id, accepted, reason); matched {
					if !accepted && !authReported {
						reportProblem(auth.attempts[pubkey].err)
						authReported = true
					}
				} else if pending != nil && !pendingWait && id == pending.event.ID.Hex() {
					if accepted {
						finishPending(nil)
					} else if strings.HasPrefix(reason, "auth-required:") && !publishRetried {
						pendingWait = true
						if err := startAuth(); err != nil {
							return failAuth(err)
						}
					} else {
						finishPending(relayRejection("relay rejected event", reason))
					}
				}
			}
		}
		if err := resume(); err != nil {
			return failAuth(err)
		}
	}
}

func (c *client) relayProblem(ctx context.Context, worker *managedRelay, err error) {
	select {
	case c.incoming <- relayEvent{worker: worker, problem: relayProblemText(err)}:
	case <-ctx.Done():
	}
}

func relayProblemText(err error) string {
	return truncateString(sanitizeString(err.Error()), 240)
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
