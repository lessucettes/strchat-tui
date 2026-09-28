package client

// Hermetic relay transport integration tests.
//
// Every test in this file runs a real client (either a bare relay worker or
// the full Run loop) against a real loopback websocket relay created with
// httptest. Nothing here dials the public network, and every client uses a
// disposable config path inside t.TempDir().
//
// The behaviours under test are the transport contract of relays.go:
//   - reconnect after a transport failure or a CLOSED frame, with the
//     subscription re-established on the new connection,
//   - subscription replacement on the same connection when chats change,
//   - removal of a relay stops its worker without resurrection, and stale
//     status events from a retired worker cannot touch the current one,
//   - malformed, duplicate and forged traffic never reaches the display,
//   - a publish is only reported as success when a relay acknowledged it,
//   - stalled peers are cancelled promptly and the frame reader is bounded,
//   - the Run loop survives rapid actions and is bounded with a blocked
//     display consumer.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/nbd-wtf/go-nostr"
)

const (
	// transportTestKey is a fixed, valid nostr secret key used to sign the
	// events pushed by the loopback relays.
	transportTestKey = "0000000000000000000000000000000000000000000000000000000000000001"

	// transportWaitOp bounds a single wire operation.
	transportWaitOp = 5 * time.Second
	// transportWaitSlow bounds connect/reconnect assertions; the worker retry
	// delay starts at 250-500 ms, so this is a generous ceiling.
	transportWaitSlow = 8 * time.Second
)

// ---------------------------------------------------------------------------
// Client/config helpers
// ---------------------------------------------------------------------------

// relayTestConfig returns a single-chat config whose anchors are the given
// loopback relay URLs and whose config file lives under t.TempDir().
func relayTestConfig(t testing.TB, anchors ...string) *config {
	t.Helper()
	return &config{
		Views:          []View{{Name: "lobby"}},
		ActiveViewName: "lobby",
		AnchorRelays:   anchors,
		path:           filepath.Join(t.TempDir(), "config.json"),
	}
}

// transportClient builds a client wired to disposable channels and a temporary
// config path, so tests never touch the real config file. Background workers
// are joined on cleanup.
func transportClient(t testing.TB, cfg *config) *client {
	t.Helper()
	if cfg.path == "" {
		cfg.path = filepath.Join(t.TempDir(), "config.json")
	}
	c := newClient(cfg, make(chan UserAction, 256), make(chan DisplayEvent, 2048))
	t.Cleanup(func() {
		c.Stop()
		done := make(chan struct{})
		go func() { c.wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(transportWaitOp):
			t.Errorf("client background workers did not exit after Stop")
		}
	})
	return c
}

// drainRelayReports keeps the bounded relay-status queue moving for tests that
// drive relay workers without running the client loop.
func drainRelayReports(t testing.TB, c *client) {
	t.Helper()
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-c.incoming:
			case <-stop:
				return
			}
		}
	}()
	t.Cleanup(func() { close(stop) })
}

// signedChatEvent signs an event for the given chat with a fixed test key.
func signedChatEvent(t testing.TB, key string, kind int, chat, content string, createdAt nostr.Timestamp, extra ...nostr.Tag) *nostr.Event {
	t.Helper()
	tagKey := "d"
	if kind == geoChatKind {
		tagKey = "g"
	}
	tags := nostr.Tags{{tagKey, chat}, {"n", "alice"}}
	tags = append(tags, extra...)
	e := &nostr.Event{Kind: kind, CreatedAt: createdAt, Tags: tags, Content: content}
	if err := e.Sign(key); err != nil {
		t.Fatalf("sign test event: %v", err)
	}
	return e
}

func relayEventFrame(subID string, ev *nostr.Event) []any { return []any{"EVENT", subID, ev} }
func relayOKFrame(id string, accepted bool, reason string) []any {
	return []any{"OK", id, accepted, reason}
}
func relayClosedFrame(subID, reason string) []any { return []any{"CLOSED", subID, reason} }

// freshChatEvent signs a brand-new event on the spot, so long-running traffic
// always falls inside the client's now-300s..now+60s freshness window and every
// event is unique. It reports failures through the relay server (handler
// goroutines must not call Fatal) and returns nil when signing fails.
func freshChatEvent(ts *relayTestServer, key, chat string, seq int) *nostr.Event {
	ev := &nostr.Event{
		Kind:      ephChatKind,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"d", chat}, {"n", "peer"}},
		Content:   fmt.Sprintf("%s-%d", chat, seq),
	}
	if err := ev.Sign(key); err != nil {
		ts.t.Errorf("signing fresh event %d for %q: %v", seq, chat, err)
		return nil
	}
	return ev
}

// waitUntil polls cond until it holds or the timeout elapses.
func waitUntil(t testing.TB, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// Loopback websocket relay
// ---------------------------------------------------------------------------

// relayTestServer is a scripted loopback websocket relay. Handlers run in the
// HTTP server's goroutines: they must report failures with Errorf and return,
// never with Fatal.
type relayTestServer struct {
	t       testing.TB
	srv     *httptest.Server
	url     string
	conns   atomic.Int32
	reqs    atomic.Int32
	closes  atomic.Int32
	events  atomic.Int32
	reqCh   chan []json.RawMessage
	eventCh chan nostr.Event
	out     chan []byte
	mu      sync.Mutex
	codes   []websocket.StatusCode
}

func newRelayWSServer(t testing.TB, handler func(ts *relayTestServer, conn *websocket.Conn, connIndex int)) *relayTestServer {
	t.Helper()
	ts := &relayTestServer{
		t:       t,
		reqCh:   make(chan []json.RawMessage, 64),
		eventCh: make(chan nostr.Event, 64),
		out:     make(chan []byte, 4),
	}
	ts.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		handler(ts, conn, int(ts.conns.Add(1)))
	}))
	t.Cleanup(ts.srv.Close)
	ts.url = "ws" + strings.TrimPrefix(ts.srv.URL, "http")
	return ts
}

// noteClose records how a connection ended. CloseStatus is -1 when the peer
// vanished without a websocket close handshake.
func (ts *relayTestServer) noteClose(err error) {
	ts.closes.Add(1)
	if code := websocket.CloseStatus(err); code != -1 {
		ts.mu.Lock()
		ts.codes = append(ts.codes, code)
		ts.mu.Unlock()
	}
}

func (ts *relayTestServer) closeCodes() []websocket.StatusCode {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return append([]websocket.StatusCode(nil), ts.codes...)
}

func (ts *relayTestServer) noteEvent(ev nostr.Event) {
	ts.events.Add(1)
	select {
	case ts.eventCh <- ev:
	default:
	}
}

// waitConns waits until the relay accepted at least n connections.
func (ts *relayTestServer) waitConns(n int32, timeout time.Duration) {
	ts.t.Helper()
	if !waitUntil(ts.t, timeout, func() bool { return ts.conns.Load() >= n }) {
		ts.t.Fatalf("relay %s: saw %d connections, want >= %d", ts.url, ts.conns.Load(), n)
	}
}

// waitREQs waits until the relay received at least n REQ frames.
func (ts *relayTestServer) waitREQs(n int32, timeout time.Duration) {
	ts.t.Helper()
	if !waitUntil(ts.t, timeout, func() bool { return ts.reqs.Load() >= n }) {
		ts.t.Fatalf("relay %s: saw %d REQ frames, want >= %d", ts.url, ts.reqs.Load(), n)
	}
}

// waitClosed waits until the relay observed at least n connections ending.
func (ts *relayTestServer) waitClosed(n int32, timeout time.Duration) {
	ts.t.Helper()
	if !waitUntil(ts.t, timeout, func() bool { return ts.closes.Load() >= n }) {
		ts.t.Fatalf("relay %s: saw %d closed connections, want >= %d", ts.url, ts.closes.Load(), n)
	}
}

// waitEvents waits until the relay received at least n EVENT frames.
func (ts *relayTestServer) waitEvents(n int32, timeout time.Duration) {
	ts.t.Helper()
	if !waitUntil(ts.t, timeout, func() bool { return ts.events.Load() >= n }) {
		ts.t.Fatalf("relay %s: saw %d EVENT frames, want >= %d", ts.url, ts.events.Load(), n)
	}
}

// takeREQ returns the next recorded REQ frame, failing the test on timeout.
func (ts *relayTestServer) takeREQ() []json.RawMessage {
	ts.t.Helper()
	select {
	case frame := <-ts.reqCh:
		return frame
	case <-time.After(transportWaitOp):
		ts.t.Fatalf("relay %s: no REQ frame recorded within %v", ts.url, transportWaitOp)
		return nil
	}
}

// waitREQContent waits until a REQ frame mentioning every one of want arrives.
func (ts *relayTestServer) waitREQContent(want ...string) []json.RawMessage {
	ts.t.Helper()
	deadline := time.Now().Add(transportWaitSlow)
	for time.Now().Before(deadline) {
		select {
		case frame := <-ts.reqCh:
			blob := string(frame[0])
			if len(frame) > 2 {
				blob = string(mustMarshal(frame[2:]))
			}
			ok := true
			for _, w := range want {
				if !strings.Contains(blob, w) {
					ok = false
				}
			}
			if ok {
				return frame
			}
		case <-time.After(5 * time.Millisecond):
		}
	}
	ts.t.Fatalf("relay %s: no REQ frame containing %v", ts.url, want)
	return nil
}

func mustMarshal(v any) []byte {
	data, _ := json.Marshal(v)
	return data
}

// ---------------------------------------------------------------------------
// Handler-side frame plumbing (never calls Fatal: handler goroutines)
// ---------------------------------------------------------------------------

// relayNextFrame reads the next JSON frame from the peer. ok=false means the
// connection ended or produced a non-JSON frame.
func relayNextFrame(ts *relayTestServer, conn *websocket.Conn) ([]json.RawMessage, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		ts.noteClose(err)
		return nil, false
	}
	var frame []json.RawMessage
	if err := json.Unmarshal(data, &frame); err != nil {
		ts.t.Errorf("relay %s: client sent a malformed frame: %v", ts.url, err)
		return nil, false
	}
	return frame, true
}

// relayNextLabeledFrame reads frames until one has the wanted label.
func relayNextLabeledFrame(ts *relayTestServer, conn *websocket.Conn, label string) ([]json.RawMessage, bool) {
	for {
		frame, ok := relayNextFrame(ts, conn)
		if !ok {
			return nil, false
		}
		var got string
		if len(frame) > 0 && json.Unmarshal(frame[0], &got) == nil && got == label {
			return frame, true
		}
	}
}

// relayTestWaitREQ waits for a REQ frame and records it.
func relayTestWaitREQ(ts *relayTestServer, conn *websocket.Conn) bool {
	frame, ok := relayNextLabeledFrame(ts, conn, "REQ")
	if !ok {
		return false
	}
	relayTestNoteREQ(ts, frame)
	return true
}

// relayTestNoteREQ records a REQ frame (first subscription or replacement).
func relayTestNoteREQ(ts *relayTestServer, frame []json.RawMessage) {
	ts.reqs.Add(1)
	select {
	case ts.reqCh <- frame:
	default:
	}
}

// relayServeAckFrames answers every EVENT with an OK frame carrying the given
// verdict and records every REQ it sees (including subscription replacements),
// so tests can assert on resubscription traffic. It returns when the peer
// disconnects.
func relayServeAckFrames(ts *relayTestServer, conn *websocket.Conn, accepted bool, reason string) {
	for {
		frame, ok := relayNextFrame(ts, conn)
		if !ok {
			return
		}
		if len(frame) < 2 {
			continue
		}
		var label string
		if json.Unmarshal(frame[0], &label) != nil {
			continue
		}
		switch label {
		case "REQ":
			relayTestNoteREQ(ts, frame)
		case "EVENT":
			var ev nostr.Event
			if json.Unmarshal(frame[1], &ev) != nil {
				continue
			}
			ts.noteEvent(ev)
			if !relayWriteJSON(ts, conn, relayOKFrame(ev.ID, accepted, reason)) {
				return
			}
		}
	}
}

// relayServeChat answers REQ, then replies to every EVENT with an OK frame
// using the requested verdict. It returns when the peer disconnects.
func relayServeChat(ts *relayTestServer, conn *websocket.Conn, accepted bool, reason string) {
	if !relayTestWaitREQ(ts, conn) {
		return
	}
	for {
		frame, ok := relayNextFrame(ts, conn)
		if !ok {
			return
		}
		var label string
		if len(frame) < 2 || json.Unmarshal(frame[0], &label) != nil || label != "EVENT" {
			continue
		}
		var ev nostr.Event
		if err := json.Unmarshal(frame[1], &ev); err != nil {
			ts.t.Errorf("relay %s: EVENT payload did not parse: %v", ts.url, err)
			return
		}
		ts.noteEvent(ev)
		if !relayWriteJSON(ts, conn, relayOKFrame(ev.ID, accepted, reason)) {
			return
		}
	}
}

// relayReadUntilClosed consumes frames until the peer goes away.
func relayReadUntilClosed(ts *relayTestServer, conn *websocket.Conn) {
	for {
		if _, ok := relayNextFrame(ts, conn); !ok {
			return
		}
	}
}

func relayWriteJSON(ts *relayTestServer, conn *websocket.Conn, v any) bool {
	data, err := json.Marshal(v)
	if err != nil {
		ts.t.Errorf("relay %s: marshal outbound frame: %v", ts.url, err)
		return false
	}
	return relayWriteRaw(ts, conn, data)
}

func relayWriteRaw(ts *relayTestServer, conn *websocket.Conn, data []byte) bool {
	ctx, cancel := context.WithTimeout(context.Background(), transportWaitOp)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		// A failed write means the peer is gone; record the close so tests can
		// assert that sockets are released even when the handler never read.
		ts.noteClose(err)
		return false
	}
	return true
}

// relayREQChats extracts the chat names from a REQ frame's filters.
func relayREQChats(t testing.TB, frame []json.RawMessage) []string {
	t.Helper()
	var chats []string
	for _, raw := range frame[2:] {
		var filter map[string]json.RawMessage
		if err := json.Unmarshal(raw, &filter); err != nil {
			continue
		}
		for key, rawTags := range filter {
			if key != "#d" && key != "#g" {
				continue
			}
			var tags []string
			if err := json.Unmarshal(rawTags, &tags); err == nil {
				chats = append(chats, tags...)
			}
		}
	}
	return chats
}

// relayREQSince returns the earliest "since" value carried by a REQ frame's
// filters.
func relayREQSince(t testing.TB, frame []json.RawMessage) (nostr.Timestamp, bool) {
	t.Helper()
	var since nostr.Timestamp
	found := false
	for _, raw := range frame[2:] {
		var filter struct {
			Since *nostr.Timestamp `json:"since"`
		}
		if json.Unmarshal(raw, &filter) != nil || filter.Since == nil {
			continue
		}
		if !found || *filter.Since < since {
			since = *filter.Since
			found = true
		}
	}
	return since, found
}

// ---------------------------------------------------------------------------
// Client harness
// ---------------------------------------------------------------------------

// relayHarness runs a real client.Run loop and records every DisplayEvent so
// tests can assert on presence, absence, counts and ordering.
type relayHarness struct {
	t         testing.TB
	c         *client
	actions   chan UserAction
	events    chan DisplayEvent
	done      chan struct{}
	collected chan struct{}
	stopOnce  sync.Once
	stopped   chan struct{}
	mu        sync.Mutex
	log       []DisplayEvent
}

func startRelayHarness(t testing.TB, cfg *config, preRun func(*client)) *relayHarness {
	t.Helper()
	if cfg.path == "" {
		cfg.path = filepath.Join(t.TempDir(), "config.json")
	}
	h := &relayHarness{
		t:         t,
		actions:   make(chan UserAction, 1024),
		events:    make(chan DisplayEvent, 4096),
		done:      make(chan struct{}),
		collected: make(chan struct{}),
		stopped:   make(chan struct{}),
	}
	h.c = newClient(cfg, h.actions, h.events)
	if preRun != nil {
		preRun(h.c)
	}
	go func() { defer close(h.done); h.c.Run() }()
	go h.collect()
	t.Cleanup(h.stop)
	return h
}

func (h *relayHarness) collect() {
	defer close(h.collected)
	for ev := range h.events {
		h.mu.Lock()
		h.log = append(h.log, ev)
		h.mu.Unlock()
	}
}

// stop cancels the client, joins Run and the collector, and is idempotent.
func (h *relayHarness) stop() {
	h.stopOnce.Do(func() {
		h.c.Stop()
		select {
		case <-h.done:
			close(h.events)
			<-h.collected
		case <-time.After(transportWaitOp):
			// Run is still alive: leave the channels alone (closing them would
			// panic on a sender) and fail loudly.
			h.t.Errorf("client Run did not return within %v of Stop", transportWaitOp)
		}
		close(h.stopped)
	})
}

// stopWithin stops the client and fails if Run does not return in time.
func (h *relayHarness) stopWithin(d time.Duration) time.Duration {
	h.t.Helper()
	start := time.Now()
	h.c.Stop()
	select {
	case <-h.done:
	case <-time.After(d):
		h.t.Fatalf("client Run did not return within %v of Stop", d)
	}
	return time.Since(start)
}

func (h *relayHarness) snapshot() []DisplayEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]DisplayEvent(nil), h.log...)
}

func (h *relayHarness) mark() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.log)
}

func (h *relayHarness) dump(limit int) string {
	events := h.snapshot()
	if len(events) > limit {
		events = events[len(events)-limit:]
	}
	var b strings.Builder
	for _, ev := range events {
		fmt.Fprintf(&b, "\n  [%s] %q", ev.Type, truncateString(ev.Content, 90))
	}
	return b.String()
}

// waitForEvent waits until an event matching pred is recorded.
func (h *relayHarness) waitForEvent(desc string, pred func(DisplayEvent) bool) DisplayEvent {
	h.t.Helper()
	deadline := time.Now().Add(transportWaitSlow)
	for {
		for _, ev := range h.snapshot() {
			if pred(ev) {
				return ev
			}
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s; events seen:%s", desc, h.dump(25))
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// assertNoEventsFrom fails if an event matching pred is recorded within window.
func (h *relayHarness) assertNoEventsFrom(mark int, window time.Duration, desc string, pred func(DisplayEvent) bool) {
	h.t.Helper()
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		events := h.snapshot()
		if mark > len(events) {
			mark = len(events)
		}
		for _, ev := range events[mark:] {
			if pred(ev) {
				h.t.Fatalf("unexpected event while waiting for %s: [%s] %q", desc, ev.Type, ev.Content)
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (h *relayHarness) send(a UserAction) {
	h.t.Helper()
	select {
	case h.actions <- a:
	case <-h.done:
		h.t.Fatalf("client Run returned before action %s could be delivered", a.Type)
	case <-time.After(transportWaitOp):
		h.t.Fatalf("action %s could not be delivered", a.Type)
	}
}

// inject pushes a relay status/event directly into the client's incoming queue.
func (h *relayHarness) inject(ev relayEvent) {
	h.t.Helper()
	select {
	case h.c.incoming <- ev:
	case <-h.done:
		h.t.Errorf("client Run already returned; cannot inject a relay event")
	case <-time.After(transportWaitOp):
		h.t.Errorf("relay event could not be injected")
	}
}

func (h *relayHarness) countType(typ string) int {
	n := 0
	for _, ev := range h.snapshot() {
		if ev.Type == typ {
			n++
		}
	}
	return n
}

func (h *relayHarness) relayStatusCount(url string, connected bool) int {
	n := 0
	for _, ev := range h.snapshot() {
		if info, ok := relayInfoFor(ev, url); ok && info.Connected == connected {
			n++
		}
	}
	return n
}

func (h *relayHarness) relayMentionsCount(url string) int {
	n := 0
	for _, ev := range h.snapshot() {
		if _, ok := relayInfoFor(ev, url); ok {
			n++
		}
	}
	return n
}

func (h *relayHarness) latestRelayStatuses() ([]RelayInfo, bool) {
	events := h.snapshot()
	for i := len(events) - 1; i >= 0; i-- {
		if statuses, ok := events[i].Payload.([]RelayInfo); ok && events[i].Type == "RELAYS_UPDATE" {
			return statuses, true
		}
	}
	return nil, false
}

func (h *relayHarness) waitRelayStatusCount(url string, connected bool, min int) {
	h.t.Helper()
	h.waitForEvent(fmt.Sprintf("relay %s connected=%v (count >= %d)", url, connected, min), func(DisplayEvent) bool {
		return h.relayStatusCount(url, connected) >= min
	})
}

// waitRelayList waits for a RELAYS_UPDATE listing every URL in present and none
// of the URLs in absent.
func (h *relayHarness) waitRelayList(present, absent []string) {
	h.t.Helper()
	h.waitForEvent(fmt.Sprintf("RELAYS_UPDATE with %v present and %v absent", present, absent), func(ev DisplayEvent) bool {
		if ev.Type != "RELAYS_UPDATE" {
			return false
		}
		statuses, ok := ev.Payload.([]RelayInfo)
		if !ok {
			return false
		}
		seen := make(map[string]bool, len(statuses))
		for _, s := range statuses {
			seen[s.URL] = true
		}
		for _, u := range present {
			if !seen[u] {
				return false
			}
		}
		for _, u := range absent {
			if seen[u] {
				return false
			}
		}
		return true
	})
}

func (h *relayHarness) waitMessage(content string) DisplayEvent {
	h.t.Helper()
	return h.waitForEvent(fmt.Sprintf("NEW_MESSAGE %q", content), messagePred(content))
}

func (h *relayHarness) countMessages(content string) int {
	n := 0
	for _, ev := range h.snapshot() {
		if messagePred(content)(ev) {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// DisplayEvent predicates
// ---------------------------------------------------------------------------

// relayInfoFor returns the RelayInfo for url if the event is a RELAYS_UPDATE
// listing it.
func relayInfoFor(ev DisplayEvent, url string) (RelayInfo, bool) {
	if ev.Type != "RELAYS_UPDATE" {
		return RelayInfo{}, false
	}
	statuses, ok := ev.Payload.([]RelayInfo)
	if !ok {
		return RelayInfo{}, false
	}
	for _, s := range statuses {
		if s.URL == url {
			return s, true
		}
	}
	return RelayInfo{}, false
}

func relayStatePred(url string, connected bool) func(DisplayEvent) bool {
	return func(ev DisplayEvent) bool {
		info, ok := relayInfoFor(ev, url)
		return ok && info.Connected == connected
	}
}

func messagePred(content string) func(DisplayEvent) bool {
	return func(ev DisplayEvent) bool { return ev.Type == "NEW_MESSAGE" && ev.Content == content }
}

func contentPred(substr string) func(DisplayEvent) bool {
	return func(ev DisplayEvent) bool { return strings.Contains(ev.Content, substr) }
}

// ---------------------------------------------------------------------------
// Reconnect / CLOSED / subscription lifecycle
// ---------------------------------------------------------------------------

func TestRelayDisconnectReconnectsAndResubscribes(t *testing.T) {
	first := signedEvent(t, "first message")
	second := signedEvent(t, "second message")
	ts := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, idx int) {
		if !relayTestWaitREQ(ts, conn) {
			return
		}
		switch idx {
		case 1:
			relayWriteJSON(ts, conn, relayEventFrame("chat", first))
			conn.CloseNow() // abrupt transport failure: the client must dial again
		default:
			relayWriteJSON(ts, conn, relayEventFrame("chat", second))
			relayReadUntilClosed(ts, conn)
		}
	})
	h := startRelayHarness(t, relayTestConfig(t, ts.url), nil)
	h.waitRelayStatusCount(ts.url, true, 1)
	h.waitMessage("first message")
	h.waitRelayStatusCount(ts.url, false, 1)
	h.waitMessage("second message")
	h.waitRelayStatusCount(ts.url, true, 2)
	ts.waitConns(2, transportWaitSlow)
	ts.waitREQs(2, transportWaitSlow)

	// The whole cycle must be visible in order: connect, first message,
	// disconnect, reconnect, second message. The second message arriving at all
	// proves the subscription was re-established on the new connection.
	//
	// Note: a first RELAYS_UPDATE with Connected=false is emitted before the
	// initial dial (the worker exists but has not connected yet), so the
	// disconnect marker must be searched for *after* the first message.
	events := h.snapshot()
	find := func(pred func(DisplayEvent) bool, from int) int {
		for i := from; i < len(events); i++ {
			if pred(events[i]) {
				return i
			}
		}
		return -1
	}
	firstConnected := find(relayStatePred(ts.url, true), 0)
	firstMsg := find(messagePred("first message"), 0)
	disconnected := find(relayStatePred(ts.url, false), firstMsg)
	secondConnected := find(relayStatePred(ts.url, true), disconnected)
	secondMsg := find(messagePred("second message"), 0)
	if !(firstConnected >= 0 && firstConnected < firstMsg && firstMsg < disconnected &&
		disconnected < secondConnected && secondConnected < secondMsg) {
		t.Fatalf("reconnect cycle out of order (connect=%d first=%d disconnect=%d reconnect=%d second=%d); events:%s",
			firstConnected, firstMsg, disconnected, secondConnected, secondMsg, h.dump(25))
	}

	// The re-subscription must not lose or rewind the catch-up window.
	reqFirst, reqSecond := ts.takeREQ(), ts.takeREQ()
	firstSince, okFirst := relayREQSince(t, reqFirst)
	secondSince, okSecond := relayREQSince(t, reqSecond)
	if !okFirst || !okSecond {
		t.Fatalf("REQ frames carry no since filter: %s | %s", mustMarshal(reqFirst), mustMarshal(reqSecond))
	}
	now := nostr.Now()
	if secondSince < firstSince {
		t.Errorf("reconnect rewound the subscription window: since %d -> %d", firstSince, secondSince)
	}
	if secondSince > now+5 || secondSince < now-300 {
		t.Errorf("reconnect subscription since %d is outside a sane window around %d", secondSince, now)
	}
}

func TestRelayClosedFrameForcesReconnect(t *testing.T) {
	recovered := signedEvent(t, "recovered after closed")
	ts := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, idx int) {
		if !relayTestWaitREQ(ts, conn) {
			return
		}
		if idx == 1 {
			// Keep the socket open: only the CLOSED frame may trigger a reconnect.
			relayWriteJSON(ts, conn, relayClosedFrame("chat", "blocked: test"))
			relayReadUntilClosed(ts, conn)
			return
		}
		relayWriteJSON(ts, conn, relayEventFrame("chat", recovered))
		relayReadUntilClosed(ts, conn)
	})
	h := startRelayHarness(t, relayTestConfig(t, ts.url), nil)
	h.waitRelayStatusCount(ts.url, true, 1)
	h.waitRelayStatusCount(ts.url, false, 1)
	h.waitMessage("recovered after closed")
	h.waitRelayStatusCount(ts.url, true, 2)
	ts.waitConns(2, transportWaitSlow)
	ts.waitREQs(2, transportWaitSlow)
}

func TestRelayClosedForOtherSubscriptionIsIgnored(t *testing.T) {
	live := signedEvent(t, "still subscribed")
	ts := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, idx int) {
		if !relayTestWaitREQ(ts, conn) {
			return
		}
		relayWriteJSON(ts, conn, relayClosedFrame("other", "not our subscription"))
		relayWriteJSON(ts, conn, relayEventFrame("chat", live))
		relayReadUntilClosed(ts, conn)
	})
	h := startRelayHarness(t, relayTestConfig(t, ts.url), nil)
	h.waitRelayStatusCount(ts.url, true, 1)
	h.waitMessage("still subscribed")
	// A CLOSED frame for a different subscription must not tear the
	// connection down: no disconnect, no reconnect, same REQ budget.
	h.assertNoEventsFrom(h.mark(), 800*time.Millisecond, "no torn-down connection", relayStatePred(ts.url, false))
	ts.waitConns(1, 200*time.Millisecond)
	ts.waitREQs(1, 100*time.Millisecond)
	if got := ts.conns.Load(); got != 1 {
		t.Fatalf("relay saw %d connections, want exactly 1", got)
	}
	if got := ts.reqs.Load(); got != 1 {
		t.Fatalf("relay saw %d REQ frames, want exactly 1", got)
	}
}

func TestRelaySubscriptionReplacementReusesConnection(t *testing.T) {
	other := signedChatEvent(t, transportTestKey, ephChatKind, "other", "hello other room", nostr.Now())
	ts := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, idx int) {
		if !relayTestWaitREQ(ts, conn) {
			return
		}
		// The active view changes; the worker must replace the subscription on
		// the same connection instead of reconnecting.
		if !relayTestWaitREQ(ts, conn) {
			return
		}
		relayWriteJSON(ts, conn, relayEventFrame("chat", other))
		relayReadUntilClosed(ts, conn)
	})
	h := startRelayHarness(t, relayTestConfig(t, ts.url), nil)
	h.waitRelayStatusCount(ts.url, true, 1)
	req1 := ts.takeREQ()
	h.send(UserAction{Type: "JOIN_CHATS", Payload: "other"})
	h.waitMessage("hello other room")
	req2 := ts.takeREQ()

	if got := ts.conns.Load(); got != 1 {
		t.Fatalf("relay saw %d connections, want 1 (subscription replacement must reuse the connection)", got)
	}
	if got := ts.reqs.Load(); got != 2 {
		t.Fatalf("relay saw %d REQ frames, want 2", got)
	}
	if chats := relayREQChats(t, req1); len(chats) != 1 || chats[0] != "lobby" {
		t.Errorf("first REQ covered %v, want [lobby]", chats)
	}
	if chats := relayREQChats(t, req2); len(chats) != 1 || chats[0] != "other" {
		t.Errorf("replacement REQ covered %v, want [other]", chats)
	}
}

func TestRelayRemovalStopsWorkerWithoutResurrection(t *testing.T) {
	drop := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, idx int) {
		if !relayTestWaitREQ(ts, conn) {
			return
		}
		relayReadUntilClosed(ts, conn)
	})
	keep := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, idx int) {
		if !relayTestWaitREQ(ts, conn) {
			return
		}
		relayReadUntilClosed(ts, conn)
	})
	h := startRelayHarness(t, relayTestConfig(t, drop.url, keep.url), nil)
	h.waitRelayStatusCount(drop.url, true, 1)
	h.waitRelayStatusCount(keep.url, true, 1)
	drop.waitConns(1, transportWaitSlow)
	keep.waitConns(1, transportWaitSlow)

	removeStart := time.Now()
	h.send(UserAction{Type: "MANAGE_ANCHORS", Payload: "1"}) // removes the first anchor
	drop.waitClosed(1, 3*time.Second)                        // removal must stop the worker promptly
	closedAfter := time.Since(removeStart)
	h.waitRelayList(nil, []string{drop.url})

	// Within a full retry window the removed relay must not be dialed again and
	// must not reappear in the display.
	mentions := h.relayMentionsCount(drop.url)
	time.Sleep(1500 * time.Millisecond)
	if got := drop.conns.Load(); got != 1 {
		t.Errorf("removed relay was dialed again: %d connections", got)
	}
	if got := h.relayMentionsCount(drop.url); got != mentions {
		t.Errorf("removed relay reappeared in RELAYS_UPDATE: %d mentions", got)
	}
	if got := keep.conns.Load(); got != 1 {
		t.Errorf("unrelated relay was disturbed: %d connections", got)
	}
	t.Logf("removed relay socket closed %v after removal", closedAfter)

	// Explicitly re-adding the relay is the only thing that brings it back.
	h.send(UserAction{Type: "MANAGE_ANCHORS", Payload: drop.url})
	drop.waitConns(2, transportWaitSlow)
	drop.waitREQs(2, transportWaitSlow)
	h.waitRelayStatusCount(drop.url, true, 2)
	h.waitRelayList([]string{drop.url, keep.url}, nil)
}

func TestRemovedWorkerStatusEventsCannotResurrectOrAffectCurrentWorker(t *testing.T) {
	ts := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, idx int) {
		relayServeChat(ts, conn, true, "")
	})
	var staleCancelled atomic.Bool
	stale := &managedRelay{
		url:     ts.url,
		cancel:  func() { staleCancelled.Store(true) },
		chats:   []string{"lobby"},
		updates: make(chan []string, 1),
		publish: make(chan relayPublish, 1),
	}
	h := startRelayHarness(t, relayTestConfig(t, ts.url), func(c *client) { c.relays[ts.url] = stale })

	// Positive control: while the worker is current, its status is honoured
	// (including the reported latency) and never dials by itself.
	h.inject(relayEvent{worker: stale, connected: true, latency: 250 * time.Millisecond})
	status := h.waitForEvent("status of the current worker", relayStatePred(ts.url, true))
	if info, ok := relayInfoFor(status, ts.url); !ok || info.Latency != 250*time.Millisecond {
		t.Errorf("injected status not surfaced faithfully: %+v", info)
	}
	if got := ts.conns.Load(); got != 0 {
		t.Fatalf("an inert worker opened %d connections", got)
	}

	// Removal must cancel the worker and drop it from the pool.
	h.send(UserAction{Type: "MANAGE_ANCHORS", Payload: "1"})
	if !waitUntil(t, transportWaitSlow, staleCancelled.Load) {
		t.Fatal("removed relay worker was not cancelled")
	}
	h.waitRelayList(nil, []string{ts.url})

	// Status events echoing the retired worker must be ignored: the URL is no
	// longer in the pool, so they must not produce a single RELAYS_UPDATE and
	// must never make the relay appear again.
	after := h.mark()
	h.inject(relayEvent{worker: stale, connected: true, latency: time.Second})
	h.inject(relayEvent{worker: stale, connected: false})
	h.assertNoEventsFrom(after, 500*time.Millisecond, "retired worker traffic", func(ev DisplayEvent) bool {
		_, present := relayInfoFor(ev, ts.url)
		return present
	})
	time.Sleep(1200 * time.Millisecond) // beyond the worker's retry delay
	if got := ts.conns.Load(); got != 0 {
		t.Errorf("unconfigured relay was dialed %d times", got)
	}

	// Re-adding creates a fresh worker that really connects.
	h.send(UserAction{Type: "MANAGE_ANCHORS", Payload: ts.url})
	ts.waitConns(1, transportWaitSlow)
	ts.waitREQs(1, transportWaitSlow)
	h.waitRelayStatusCount(ts.url, true, 2)

	// A stale "disconnected" for the retired worker must not mark the fresh one
	// down: no disconnected status may appear, and the relay must stay usable.
	afterFresh := h.mark()
	h.inject(relayEvent{worker: stale, connected: false})
	h.assertNoEventsFrom(afterFresh, 400*time.Millisecond, "stale disconnect", func(ev DisplayEvent) bool {
		info, ok := relayInfoFor(ev, ts.url)
		return ok && !info.Connected
	})
	h.send(UserAction{Type: "SEND_MESSAGE", Payload: "ping after stale status"})
	h.waitForEvent("publish acknowledged after stale status", func(ev DisplayEvent) bool {
		return ev.Type == "STATUS" && strings.Contains(ev.Content, "acknowledged by 1/1")
	})
	ts.waitEvents(1, transportWaitSlow)
}

// ---------------------------------------------------------------------------
// Malformed, duplicate and forged traffic
// ---------------------------------------------------------------------------

func TestRelayDropsMalformedDuplicateAndForgedTraffic(t *testing.T) {
	alpha := signedEvent(t, "alpha")
	beta := signedEvent(t, "beta")
	gamma := signedEvent(t, "gamma")

	tamperedContent := *alpha
	tamperedContent.Content = "alpha content swapped after signing"
	tamperedSig := *alpha
	tamperedSig.Sig = strings.Repeat("a", 128)
	kindOne := signedChatEvent(t, transportTestKey, 1, "lobby", "wrong kind", nostr.Now())
	wrongChat := signedChatEvent(t, transportTestKey, ephChatKind, "elsewhere", "wrong chat", nostr.Now())
	future := signedChatEvent(t, transportTestKey, ephChatKind, "lobby", "future event", nostr.Now()+3600)
	expired := signedChatEvent(t, transportTestKey, ephChatKind, "lobby", "expired event", nostr.Now(),
		nostr.Tag{"expiration", "1"})

	ts := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, idx int) {
		if !relayTestWaitREQ(ts, conn) {
			return
		}
		frames := [][]byte{
			[]byte("this is not json at all"),
			[]byte("[1,2,3]"),
			[]byte(`["EVENT","chat"]`),
			[]byte(`["EVENT","chat",42]`),
			[]byte(`["BOGUS","chat",{}]`),
			mustMarshal(relayEventFrame("other-sub", alpha)),
			mustMarshal(relayEventFrame("chat", alpha)),
			mustMarshal(relayEventFrame("chat", alpha)), // duplicate
			mustMarshal(relayEventFrame("chat", &tamperedContent)),
			mustMarshal(relayEventFrame("chat", &tamperedSig)),
			mustMarshal(relayEventFrame("chat", kindOne)),
			mustMarshal(relayEventFrame("chat", wrongChat)),
			mustMarshal(relayEventFrame("chat", future)),
			mustMarshal(relayEventFrame("chat", expired)),
			mustMarshal(relayEventFrame("chat", beta)),
			mustMarshal(relayOKFrame(alpha.ID, true, "")), // unsolicited OK, no pending publish
			mustMarshal(relayEventFrame("chat", gamma)),
		}
		for _, frame := range frames {
			if !relayWriteRaw(ts, conn, frame) {
				return
			}
		}
		relayReadUntilClosed(ts, conn)
	})
	h := startRelayHarness(t, relayTestConfig(t, ts.url), nil)
	h.waitRelayStatusCount(ts.url, true, 1)
	h.waitMessage("alpha")
	h.waitMessage("beta")
	h.waitMessage("gamma")

	// Exactly the three legitimate messages were displayed, each exactly once;
	// malformed, mis-addressed, duplicated and forged frames produced nothing.
	if got := h.countType("NEW_MESSAGE"); got != 3 {
		t.Fatalf("displayed %d messages, want 3; events:%s", got, h.dump(25))
	}
	for _, content := range []string{"alpha", "beta", "gamma"} {
		if got := h.countMessages(content); got != 1 {
			t.Errorf("message %q displayed %d times, want 1", content, got)
		}
	}
	if got := h.countMessages("alpha content swapped after signing"); got != 0 {
		t.Errorf("tampered event was displayed %d times", got)
	}
	// The relay stayed connected through the whole hostile burst.
	if got := ts.conns.Load(); got != 1 {
		t.Errorf("relay saw %d connections, want 1 (malformed traffic must not drop the connection)", got)
	}
	if got := ts.reqs.Load(); got != 1 {
		t.Errorf("relay saw %d REQ frames, want 1", got)
	}
}

func TestDuplicateEventFromTwoRelaysDisplayedOnce(t *testing.T) {
	dup := signedEvent(t, "shared across relays")
	makeRelay := func() *relayTestServer {
		return newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, idx int) {
			if !relayTestWaitREQ(ts, conn) {
				return
			}
			relayWriteJSON(ts, conn, relayEventFrame("chat", dup))
			relayReadUntilClosed(ts, conn)
		})
	}
	tsA := makeRelay()
	tsB := makeRelay()
	h := startRelayHarness(t, relayTestConfig(t, tsA.url, tsB.url), nil)
	h.waitRelayStatusCount(tsA.url, true, 1)
	h.waitRelayStatusCount(tsB.url, true, 1)
	h.waitMessage("shared across relays")
	h.assertNoEventsFrom(h.mark(), 500*time.Millisecond, "no second delivery", messagePred("shared across relays"))
	if got := h.countMessages("shared across relays"); got != 1 {
		t.Fatalf("event seen on two relays displayed %d times, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// Publish acknowledgement semantics
// ---------------------------------------------------------------------------

func TestPublishAckOnLoopbackRelayReportsSuccess(t *testing.T) {
	ts := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, idx int) {
		relayServeChat(ts, conn, true, "")
	})
	h := startRelayHarness(t, relayTestConfig(t, ts.url), nil)
	h.waitRelayStatusCount(ts.url, true, 1)
	ts.waitREQs(1, transportWaitSlow)
	h.send(UserAction{Type: "SEND_MESSAGE", Payload: "live message"})

	h.waitForEvent("publish acknowledgement", func(ev DisplayEvent) bool {
		return ev.Type == "STATUS" && strings.Contains(ev.Content, "acknowledged by 1/1")
	})
	own := h.waitMessage("live message")
	if !own.IsOwnMessage {
		t.Errorf("own message not marked as such: %+v", own)
	}
	ts.waitEvents(1, transportWaitSlow)
	got, ok := <-ts.eventCh
	if !ok || got.Content != "live message" {
		t.Errorf("relay received %q, want %q", got.Content, "live message")
	}
}

func TestPublishRejectedByRelayIsNotSuccess(t *testing.T) {
	ts := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, idx int) {
		relayServeChat(ts, conn, false, "blocked: spam")
	})
	h := startRelayHarness(t, relayTestConfig(t, ts.url), nil)
	h.waitRelayStatusCount(ts.url, true, 1)
	ts.waitREQs(1, transportWaitSlow)
	h.send(UserAction{Type: "SEND_MESSAGE", Payload: "reject me"})

	ev := h.waitForEvent("publish rejection", func(ev DisplayEvent) bool {
		return ev.Type == "ERROR" && strings.Contains(ev.Content, "relay rejected event: blocked: spam")
	})
	if !strings.Contains(ev.Content, "acknowledged by 0/1") {
		t.Errorf("rejection did not count zero acknowledgements: %q", ev.Content)
	}
	// A rejected publish is never a success: no STATUS, no echoed message.
	h.assertNoEventsFrom(h.mark(), 300*time.Millisecond, "no success for a rejected publish", func(ev DisplayEvent) bool {
		return strings.Contains(ev.Content, "acknowledged by 1") || messagePred("reject me")(ev)
	})
	if got := h.countMessages("reject me"); got != 0 {
		t.Errorf("rejected message displayed %d times", got)
	}
	ts.waitEvents(1, transportWaitSlow)
}

func TestPublishDisconnectBeforeAckIsNotSuccess(t *testing.T) {
	ts := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, idx int) {
		if !relayTestWaitREQ(ts, conn) {
			return
		}
		for {
			frame, ok := relayNextFrame(ts, conn)
			if !ok {
				return
			}
			var label string
			if len(frame) < 2 || json.Unmarshal(frame[0], &label) != nil || label != "EVENT" {
				continue
			}
			var ev nostr.Event
			if err := json.Unmarshal(frame[1], &ev); err != nil {
				return
			}
			ts.noteEvent(ev)
			conn.CloseNow() // drop the peer instead of acknowledging
			return
		}
	})
	h := startRelayHarness(t, relayTestConfig(t, ts.url), nil)
	h.waitRelayStatusCount(ts.url, true, 1)
	ts.waitREQs(1, transportWaitSlow)
	h.send(UserAction{Type: "SEND_MESSAGE", Payload: "lost message"})

	ev := h.waitForEvent("failure report for a dropped ack", func(ev DisplayEvent) bool {
		return ev.Type == "ERROR" && strings.Contains(ev.Content, "acknowledged by 0/1")
	})
	if strings.Contains(ev.Content, "acknowledged by 1") {
		t.Errorf("disconnect before ack reported success: %q", ev.Content)
	}
	if got := h.countMessages("lost message"); got != 0 {
		t.Errorf("unacknowledged message displayed %d times", got)
	}
	// No automatic resend: the relay must not receive the event again.
	seen := ts.events.Load()
	h.assertNoEventsFrom(h.mark(), 600*time.Millisecond, "no resend after failure", messagePred("lost message"))
	if got := ts.events.Load(); got != seen {
		t.Errorf("event was resent %d times after a failed publish", got-seen)
	}
}

func TestPublishWithoutAckNeverReportsSuccess(t *testing.T) {
	ts := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, idx int) {
		if !relayTestWaitREQ(ts, conn) {
			return
		}
		for {
			frame, ok := relayNextFrame(ts, conn)
			if !ok {
				return
			}
			var label string
			if len(frame) < 2 || json.Unmarshal(frame[0], &label) != nil || label != "EVENT" {
				continue
			}
			var ev nostr.Event
			if err := json.Unmarshal(frame[1], &ev); err != nil {
				return
			}
			ts.noteEvent(ev) // swallow the event: never acknowledge it
		}
	})
	c := transportClient(t, relayTestConfig(t, ts.url))
	drainRelayReports(t, c)
	c.updateRelaySubscriptions(map[string][]string{ts.url: {"lobby"}})
	ts.waitREQs(1, transportWaitSlow)
	worker := c.relays[ts.url]
	if worker == nil {
		t.Fatal("relay worker missing from the pool")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	job := publishJob{
		key:  transportTestKey,
		chat: "lobby",
		event: nostr.Event{
			Kind:    ephChatKind,
			Content: "never acknowledged",
			Tags:    nostr.Tags{{"d", "lobby"}},
		},
		relays: []*managedRelay{worker},
	}
	start := time.Now()
	result := executePublish(ctx, job)
	elapsed := time.Since(start)

	if result.accepted != 0 || result.kind != "ERROR" {
		t.Fatalf("unacknowledged publish reported %s with %d acknowledgements", result.kind, result.accepted)
	}
	// The missing acknowledgement is surfaced either by executePublish's own
	// deadline ("acknowledgement deadline exceeded") or by serveRelay resolving
	// the pending publish when its context expires ("context deadline
	// exceeded"); both are deadline reports, and neither may be a success.
	if !strings.Contains(result.message, "deadline exceeded") {
		t.Errorf("missing acknowledgement not surfaced as a deadline: %q", result.message)
	}
	if !strings.Contains(result.message, "acknowledged by 0/1") {
		t.Errorf("missing acknowledgement counted as success: %q", result.message)
	}
	if elapsed > 3*time.Second {
		t.Errorf("acknowledgement deadline took %v, want under 3s", elapsed)
	}
	ts.waitEvents(1, transportWaitSlow) // the event really was written to the relay
}

// ---------------------------------------------------------------------------
// Stalled peers and cancellation
// ---------------------------------------------------------------------------

func TestStalledPeerCancellationIsBounded(t *testing.T) {
	stall := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, idx int) {
		// A silently stalled peer: it never sends a single frame, keeps the
		// socket open, and only notices the close when the client tears it down.
		relayReadUntilClosed(ts, conn)
	})
	c := transportClient(t, relayTestConfig(t, stall.url))
	drainRelayReports(t, c)
	c.updateRelaySubscriptions(map[string][]string{stall.url: {"lobby"}})
	stall.waitConns(1, transportWaitSlow)
	worker := c.relays[stall.url]
	if worker == nil {
		t.Fatal("relay worker missing from the pool")
	}

	// A publish in flight when the peer stalls must be resolved as an error by
	// cancellation, never as success.
	answer := make(chan error, 1)
	select {
	case worker.publish <- relayPublish{ctx: context.Background(), event: nostr.Event{ID: strings.Repeat("c", 64)}, result: answer}:
	case <-time.After(transportWaitOp):
		t.Fatal("publish request was not accepted by the stalled worker")
	}
	time.Sleep(150 * time.Millisecond) // let serveRelay register the pending publish
	start := time.Now()
	worker.cancel()
	select {
	case err := <-answer:
		if err == nil {
			t.Fatal("stalled relay reported publish success")
		}
		if !strings.Contains(err.Error(), "disconnected") {
			t.Errorf("pending publish resolved with %v, want a disconnect report", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not resolve the pending publish within 2s")
	}
	// The worker goroutine must exit and the socket must be released promptly.
	joined := make(chan struct{})
	go func() { c.wg.Wait(); close(joined) }()
	select {
	case <-joined:
	case <-time.After(2 * time.Second):
		t.Fatal("relay worker did not exit within 2s of cancellation")
	}
	stall.waitClosed(1, 2*time.Second)
	t.Logf("stalled peer cancelled and socket closed in %v", time.Since(start))
}

func TestClientRunShutdownBoundedWithStalledRelay(t *testing.T) {
	stall := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, idx int) {
		relayReadUntilClosed(ts, conn) // never sends a frame
	})
	h := startRelayHarness(t, relayTestConfig(t, stall.url), nil)
	h.waitRelayStatusCount(stall.url, true, 1)
	start := time.Now()
	h.stopWithin(2 * time.Second)
	t.Logf("Run returned %v after Stop with a stalled relay", time.Since(start))
	stall.waitClosed(1, 2*time.Second)
}

func TestClientRunShutdownBoundedDuringConnectStall(t *testing.T) {
	var requests atomic.Int32
	gate := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		select {
		case <-r.Context().Done():
		case <-gate:
		}
	}))
	t.Cleanup(func() {
		close(gate)
		srv.Close()
	})
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	events := make(chan DisplayEvent, 16)
	actions := make(chan UserAction, 16)
	c := newClient(relayTestConfig(t, url), actions, events)
	done := make(chan struct{})
	go func() { defer close(done); c.Run() }()
	t.Cleanup(func() {
		c.Stop()
		select {
		case <-done:
		case <-time.After(transportWaitOp):
			t.Errorf("client Run leaked during connect stall")
		}
	})
	if !waitUntil(t, transportWaitSlow, func() bool { return requests.Load() >= 1 }) {
		t.Fatal("client never attempted to connect")
	}
	start := time.Now()
	c.Stop()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s while the connect was stalled")
	}
	t.Logf("Run returned %v after Stop during a stalled connect (dial budget is %v)", time.Since(start), connectTimeout)
}

// ---------------------------------------------------------------------------
// Bounded frame reader
// ---------------------------------------------------------------------------

func TestOversizedFrameIsRejectedByTheBoundedReader(t *testing.T) {
	survivor := signedEvent(t, "after oversized frame")
	oversized := bytes.Repeat([]byte("x"), maxFrameBytes+1024)
	ts := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, idx int) {
		if !relayTestWaitREQ(ts, conn) {
			return
		}
		if idx == 1 {
			relayWriteRaw(ts, conn, oversized)
			relayReadUntilClosed(ts, conn)
			return
		}
		relayWriteJSON(ts, conn, relayEventFrame("chat", survivor))
		relayReadUntilClosed(ts, conn)
	})
	h := startRelayHarness(t, relayTestConfig(t, ts.url), nil)
	h.waitRelayStatusCount(ts.url, true, 1)
	// The frame is larger than the 64 KiB cap: the connection must be torn down
	// by the bounded reader, then the worker reconnects.
	h.waitRelayStatusCount(ts.url, false, 1)
	h.waitMessage("after oversized frame")
	h.waitRelayStatusCount(ts.url, true, 2)
	ts.waitConns(2, transportWaitSlow)
	if got := h.countType("NEW_MESSAGE"); got != 1 {
		t.Fatalf("oversized frame produced %d messages, want none beyond the survivor", got-1)
	}
	// The relay should observe the peer closing with StatusMessageTooBig.
	want := int(websocket.StatusMessageTooBig)
	found := false
	for _, code := range ts.closeCodes() {
		if int(code) == want {
			found = true
		}
	}
	if !found {
		t.Errorf("relay saw close codes %v, want %d (message too big)", ts.closeCodes(), want)
	}
}

// ---------------------------------------------------------------------------
// Run loop: rapid actions, blocked consumers
// ---------------------------------------------------------------------------

func TestRunSurvivesRapidActionsAndRelayChurn(t *testing.T) {
	key := transportTestKey
	var churnSeq, stableSeq atomic.Int64
	churn := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, idx int) {
		if !relayTestWaitREQ(ts, conn) {
			return
		}
		if ev := freshChatEvent(ts, key, "lobby", int(churnSeq.Add(1))); ev != nil {
			relayWriteJSON(ts, conn, relayEventFrame("chat", ev))
		}
		time.Sleep(time.Duration(10+15*(idx%3)) * time.Millisecond)
		conn.CloseNow()
	})
	// The stable relay keeps pushing freshly signed traffic for the whole test.
	stable := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, idx int) {
		if !relayTestWaitREQ(ts, conn) {
			return
		}
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			ticker := time.NewTicker(15 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					ev := freshChatEvent(ts, key, "lobby", int(stableSeq.Add(1)))
					if ev == nil || !relayWriteJSON(ts, conn, relayEventFrame("chat", ev)) {
						return
					}
				}
			}
		}()
		relayReadUntilClosed(ts, conn)
	})
	h := startRelayHarness(t, relayTestConfig(t, churn.url, stable.url), nil)
	h.waitRelayStatusCount(stable.url, true, 1)
	h.waitRelayStatusCount(churn.url, true, 1)
	// One message delivered before the storm, so post-storm delivery is a
	// meaningful change rather than the very first event.
	h.waitForEvent("first pushed message", func(ev DisplayEvent) bool { return ev.Type == "NEW_MESSAGE" })
	before := h.countType("NEW_MESSAGE")

	actions := []UserAction{
		{Type: "JOIN_CHATS", Payload: "pulse"},
		{Type: "JOIN_CHATS", Payload: "other"},
		{Type: "ACTIVATE_VIEW", Payload: "lobby"},
		{Type: "ACTIVATE_VIEW", Payload: "other"},
		{Type: "CREATE_GROUP", Payload: "lobby,other"},
		{Type: "SET_NICK", Payload: "racer"},
		{Type: "SET_NICK", Payload: ""},
		{Type: "SET_POW", Payload: "1"},
		{Type: "SET_POW", Payload: "0"},
		{Type: "LIST_CHATS", Payload: ""},
		{Type: "GET_ACTIVE_CHAT", Payload: ""},
		{Type: "REQUEST_NICK_COMPLETION", Payload: "@"},
		{Type: "BLOCK_USER", Payload: "@nobody"},
		{Type: "UNBLOCK_USER", Payload: "1"},
		{Type: "LIST_BLOCKED", Payload: ""},
		{Type: "HANDLE_FILTER", Payload: "noise"},
		{Type: "REMOVE_FILTER", Payload: "1"},
		{Type: "CLEAR_FILTERS", Payload: ""},
		{Type: "HANDLE_MUTE", Payload: "muted"},
		{Type: "REMOVE_MUTE", Payload: "1"},
		{Type: "CLEAR_MUTES", Payload: ""},
		{Type: "MANAGE_ANCHORS", Payload: ""},
		{Type: "SEND_MESSAGE", Payload: "race message"},
	}
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 30; i++ {
				a := actions[(worker*7+i)%len(actions)]
				select {
				case h.actions <- a:
				case <-h.done:
					return
				case <-time.After(transportWaitOp):
					t.Errorf("action %s could not be delivered", a.Type)
					return
				}
			}
		}(worker)
	}
	wg.Wait()

	// The loop must still be alive after the storm.
	select {
	case <-h.done:
		t.Fatalf("Run returned during the action storm; events:%s", h.dump(25))
	default:
	}
	// The flappy relay must really reconnect while the client is hammered.
	churn.waitConns(2, transportWaitSlow)
	// Reset anything the storm toggled that would hide traffic, then prove new
	// messages keep flowing.
	h.send(UserAction{Type: "CLEAR_FILTERS", Payload: ""})
	h.send(UserAction{Type: "CLEAR_MUTES", Payload: ""})
	h.send(UserAction{Type: "ACTIVATE_VIEW", Payload: "lobby"})
	if !waitUntil(t, transportWaitSlow, func() bool { return h.countType("NEW_MESSAGE") > before }) {
		t.Fatalf("no message displayed after the storm; events:%s", h.dump(25))
	}
	h.send(UserAction{Type: "GET_ACTIVE_CHAT", Payload: ""})
	h.waitForEvent("active chat answer after the storm", func(ev DisplayEvent) bool {
		return ev.Type == "INFO" && strings.Contains(ev.Content, "active chat")
	})
	if h.countType("STATE_UPDATE") == 0 {
		t.Error("no STATE_UPDATE was emitted through the storm")
	}
	elapsed := h.stopWithin(3 * time.Second)
	t.Logf("Run returned %v after Stop following %d relay churn connections", elapsed, churn.conns.Load())
}

func TestRunShutdownWithBlockedConsumerIsBounded(t *testing.T) {
	t.Run("saturated buffer with live socket", func(t *testing.T) {
		key := transportTestKey
		ts := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, idx int) {
			if !relayTestWaitREQ(ts, conn) {
				return
			}
			// Keep pushing unique traffic so the display has something to block on.
			for i := 0; i < 400; i++ {
				ev := freshChatEvent(ts, key, "lobby", i)
				if ev == nil || !relayWriteJSON(ts, conn, relayEventFrame("chat", ev)) {
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
			relayReadUntilClosed(ts, conn)
		})
		events := make(chan DisplayEvent, 8)
		actions := make(chan UserAction, 8)
		c := newClient(relayTestConfig(t, ts.url), actions, events)
		done := make(chan struct{})
		go func() { defer close(done); c.Run() }()
		t.Cleanup(func() {
			c.Stop()
			select {
			case <-done:
			case <-time.After(transportWaitOp):
				t.Errorf("client Run leaked with a blocked consumer")
			}
		})
		// The consumer is gated: a reader drains events until the relay
		// connection and its subscription are provably established (the relay
		// has seen the REQ), and only then is the consumer removed. The display
		// therefore blocks with a real, live socket and queued relay traffic -
		// no assumption about early event counts.
		var drained atomic.Int64
		gate := make(chan struct{})
		readerDone := make(chan struct{})
		go func() {
			defer close(readerDone)
			for {
				select {
				case <-events:
					drained.Add(1)
				case <-gate:
					return
				}
			}
		}()
		ts.waitREQs(1, transportWaitSlow)
		close(gate)
		<-readerDone
		if !waitUntil(t, 3*time.Second, func() bool { return len(events) == cap(events) }) {
			t.Fatalf("display buffer holds %d of %d events, want it saturated (drained %d)", len(events), cap(events), drained.Load())
		}
		select {
		case <-done:
			t.Fatal("Run returned before Stop")
		case <-time.After(100 * time.Millisecond):
		}
		start := time.Now()
		c.Stop()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Run did not return within 2s of Stop with a blocked consumer")
		}
		t.Logf("Run returned %v after Stop with a blocked consumer (drained %d events before gating)", time.Since(start), drained.Load())
		ts.waitClosed(1, 2*time.Second) // the socket is released too
	})

	t.Run("saturated display still starts the transport", func(t *testing.T) {
		key := transportTestKey
		ts := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, idx int) {
			if !relayTestWaitREQ(ts, conn) {
				return
			}
			if ev := freshChatEvent(ts, key, "lobby", 1); ev != nil {
				relayWriteJSON(ts, conn, relayEventFrame("chat", ev))
			}
			relayReadUntilClosed(ts, conn)
		})
		// The display is full before Run even starts: relay workers must not
		// depend on the UI draining its queue, or a saturated display would
		// leave the client with no dial, no subscription and no traffic.
		events := make(chan DisplayEvent, 2)
		events <- DisplayEvent{Type: "NEW_MESSAGE", Content: "pre-filled one"}
		events <- DisplayEvent{Type: "NEW_MESSAGE", Content: "pre-filled two"}
		actions := make(chan UserAction, 8)
		c := newClient(relayTestConfig(t, ts.url), actions, events)
		done := make(chan struct{})
		go func() { defer close(done); c.Run() }()
		t.Cleanup(func() {
			c.Stop()
			select {
			case <-done:
			case <-time.After(transportWaitOp):
				t.Errorf("client Run leaked with a pre-saturated display")
			}
		})
		ts.waitREQs(1, transportWaitSlow)
		if len(events) != cap(events) {
			t.Fatalf("display buffer holds %d of %d events, want it still saturated", len(events), cap(events))
		}
		// Draining releases the parked emits; the client stays up.
		for i := 0; i < cap(events); i++ {
			select {
			case <-events:
			case <-time.After(transportWaitOp):
				t.Fatal("no display event after draining the pre-filled buffer")
			}
		}
		select {
		case <-done:
			t.Fatal("Run returned before Stop")
		default:
		}
	})

	t.Run("blocked at the very first emit", func(t *testing.T) {
		events := make(chan DisplayEvent) // unbuffered and never read
		actions := make(chan UserAction, 8)
		c := newClient(relayTestConfig(t, "ws://127.0.0.1:1/never-dialed"), actions, events)
		done := make(chan struct{})
		go func() { defer close(done); c.Run() }()
		t.Cleanup(func() {
			c.Stop()
			select {
			case <-done:
			case <-time.After(transportWaitOp):
				t.Errorf("client Run leaked at the first emit")
			}
		})
		select {
		case <-done:
			t.Fatal("Run returned before Stop")
		case <-time.After(200 * time.Millisecond):
		}
		start := time.Now()
		c.Stop()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Run did not return within 2s of Stop when blocked at the first emit")
		}
		t.Logf("Run returned %v after Stop when blocked at the first emit", time.Since(start))
	})
}

// ---------------------------------------------------------------------------
// Benchmarks (loopback sockets, hermetic)
// ---------------------------------------------------------------------------

// BenchmarkRelayConnectSubscribeCycle measures a full dial + subscribe +
// teardown cycle against a loopback relay.
func BenchmarkRelayConnectSubscribeCycle(b *testing.B) {
	ts := newRelayWSServer(b, func(ts *relayTestServer, conn *websocket.Conn, idx int) {
		if !relayTestWaitREQ(ts, conn) {
			return
		}
		relayReadUntilClosed(ts, conn)
	})
	c := transportClient(b, relayTestConfig(b))
	drainRelayReports(b, c)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 1; b.Loop(); i++ {
		c.updateRelaySubscriptions(map[string][]string{ts.url: {"lobby"}})
		if !waitUntil(b, transportWaitOp, func() bool { return ts.reqs.Load() >= int32(i) }) {
			b.Fatalf("REQ %d not received", i)
		}
		c.updateRelaySubscriptions(map[string][]string{})
		if !waitUntil(b, transportWaitOp, func() bool { return ts.closes.Load() >= int32(i) }) {
			b.Fatalf("connection %d did not close", i)
		}
	}
}

// BenchmarkEventFrameIngest measures the end-to-end cost of one relay frame
// reaching the client's queue on an established connection.
func BenchmarkEventFrameIngest(b *testing.B) {
	ts := newRelayWSServer(b, func(ts *relayTestServer, conn *websocket.Conn, idx int) {
		if !relayTestWaitREQ(ts, conn) {
			return
		}
		for {
			select {
			case data := <-ts.out:
				ctx, cancel := context.WithTimeout(context.Background(), transportWaitOp)
				err := conn.Write(ctx, websocket.MessageText, data)
				cancel()
				if err != nil {
					return
				}
			case <-time.After(60 * time.Second):
				return
			}
		}
	})
	c := transportClient(b, relayTestConfig(b))
	c.updateRelaySubscriptions(map[string][]string{ts.url: {"lobby"}})
	ts.waitREQs(1, transportWaitSlow)

	got := make(chan struct{}, 1)
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case ev := <-c.incoming:
				if ev.event == nil {
					continue
				}
				select {
				case got <- struct{}{}:
				default:
				}
			case <-stop:
				return
			}
		}
	}()
	b.Cleanup(func() { close(stop) })

	ev := signedEvent(b, "ingest benchmark event")
	frame := mustMarshal(relayEventFrame("chat", ev))
	b.SetBytes(int64(len(frame)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		ts.out <- frame
		select {
		case <-got:
		case <-time.After(transportWaitOp):
			b.Fatal("event frame was not ingested within the wire budget")
		}
	}
}

// BenchmarkPublishAckRoundTrip measures one publish request answered by a
// loopback relay's OK frame.
func BenchmarkPublishAckRoundTrip(b *testing.B) {
	ts := newRelayWSServer(b, func(ts *relayTestServer, conn *websocket.Conn, idx int) {
		relayServeChat(ts, conn, true, "")
	})
	c := transportClient(b, relayTestConfig(b))
	drainRelayReports(b, c)
	c.updateRelaySubscriptions(map[string][]string{ts.url: {"lobby"}})
	ts.waitREQs(1, transportWaitSlow)
	worker := c.relays[ts.url]
	if worker == nil {
		b.Fatal("relay worker missing from the pool")
	}
	event := nostr.Event{ID: strings.Repeat("a", 64), PubKey: strings.Repeat("b", 64), Kind: ephChatKind, Content: "bench"}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		answer := make(chan error, 1)
		select {
		case worker.publish <- relayPublish{ctx: context.Background(), event: event, result: answer}:
		case <-time.After(transportWaitOp):
			b.Fatal("publish request was not accepted")
		}
		select {
		case err := <-answer:
			if err != nil {
				b.Fatalf("loopback relay rejected the event: %v", err)
			}
		case <-time.After(transportWaitOp):
			b.Fatal("publish acknowledgement not received")
		}
	}
}
