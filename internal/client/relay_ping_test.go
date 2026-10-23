package client

// Liveness regressions for the relay idle probe. A relay can be healthy while
// the reader is parked handing us frames the owner has not drained; its exact
// pong then waits behind that data. The probe must not turn that backpressure
// into a disconnect, but a genuinely idle relay that stops answering must still
// be retired.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gorillaws "github.com/gorilla/websocket"
	"github.com/nbd-wtf/go-nostr"
)

// relayPingPeer is a loopback relay that answers a ping only after writing a burst
// of data frames, so the matching pong is queued behind them. It is a real
// socket peer, including gorilla's control-frame handling.
type relayPingPeer struct {
	url        string
	subscribed chan struct{}
	pinged     chan struct{}
	closed     chan struct{}
	burst      [][]byte
	sendPong   bool
	writer     chan []byte
}

func newRelayPingPeer(t *testing.T, burst [][]byte, sendPong bool) *relayPingPeer {
	t.Helper()
	peer := &relayPingPeer{
		pinged:     make(chan struct{}, 8),
		closed:     make(chan struct{}),
		subscribed: make(chan struct{}),
		burst:      burst,
		sendPong:   sendPong,
		writer:     make(chan []byte, 8),
	}
	upgrader := gorillaws.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() {
			_ = conn.Close()
			close(peer.closed)
		}()
		// The worker's first frame is REQ; no AUTH is involved in these tests.
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		close(peer.subscribed)
		tokens := make(chan string, 1)
		conn.SetPingHandler(func(appData string) error {
			select {
			case tokens <- appData:
			default:
			}
			return nil
		})
		writeDone := make(chan struct{})
		defer close(writeDone)
		go func() {
			for {
				select {
				case <-writeDone:
					return
				case token := <-tokens:
					for _, frame := range peer.burst {
						_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
						if err := conn.WriteMessage(gorillaws.TextMessage, frame); err != nil {
							return
						}
					}
					if peer.sendPong {
						// Written last: the pong is behind the burst in the stream.
						if err := conn.WriteControl(gorillaws.PongMessage, []byte(token), time.Now().Add(time.Second)); err != nil {
							return
						}
					}
					select {
					case peer.pinged <- struct{}{}:
					default:
					}
				case frame := <-peer.writer:
					_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
					if err := conn.WriteMessage(gorillaws.TextMessage, frame); err != nil {
						return
					}
				}
			}
		}()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	peer.url = "ws" + strings.TrimPrefix(server.URL, "http")
	return peer
}

type relayPingServeResult struct {
	done chan struct{}
	ok   atomic.Bool
}

// startRelayPingServe runs one serve loop with a test-driven probe cadence. incoming
// may replace the client queue to model a stalled owner.
func startRelayPingServe(t *testing.T, c *client, peer *relayPingPeer, pongWait time.Duration, incoming chan relayEvent) (context.CancelFunc, *relayPingServeResult, chan time.Time) {
	t.Helper()
	if incoming != nil {
		c.incoming = incoming
	}
	ctx, cancel := context.WithCancel(c.ctx)
	conn, err := dialRelayTransport(ctx, peer.url, nil, connectTimeout)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	worker := &managedRelay{url: peer.url, updates: make(chan relaySubscription, 1), publish: make(chan relayPublish, 1)}
	sub := relaySubscription{chats: []string{"lobby"}, since: nostr.Now()}
	result := &relayPingServeResult{done: make(chan struct{})}
	ticks := make(chan time.Time)
	go func() {
		_, ok := c.serveRelayWithPing(ctx, worker, conn, sub, 0, ticks, pongWait)
		result.ok.Store(ok)
		close(result.done)
	}()
	return cancel, result, ticks
}

func stopRelayPingServe(t *testing.T, cancel context.CancelFunc, result *relayPingServeResult) {
	t.Helper()
	cancel()
	select {
	case <-result.done:
	case <-time.After(2 * time.Second):
		t.Fatal("serveRelayWithPing did not exit after cancellation")
	}
}

func sendRelayPingTick(t *testing.T, ticks chan time.Time, result *relayPingServeResult) {
	t.Helper()
	select {
	case ticks <- time.Now():
	case <-result.done:
		t.Fatal("serve loop exited before accepting a tick")
	case <-time.After(2 * time.Second):
		t.Fatal("serve loop did not accept a tick")
	}
}

func waitRelayPing(t *testing.T, peer *relayPingPeer) {
	t.Helper()
	select {
	case <-peer.pinged:
	case <-time.After(3 * time.Second):
		t.Fatal("relay never received an idle ping")
	}
}

func relayEventFrames(t *testing.T, count int, prefix string) [][]byte {
	t.Helper()
	frames := make([][]byte, count)
	for i := range frames {
		event := signedEvent(t, fmt.Sprintf("%s-%d", prefix, i))
		data, err := json.Marshal([]any{"EVENT", "chat", event})
		if err != nil {
			t.Fatal(err)
		}
		frames[i] = data
	}
	return frames
}

func waitRelayEvents(t *testing.T, incoming <-chan relayEvent, want int, timeout time.Duration) []*nostr.Event {
	t.Helper()
	deadline := time.After(timeout)
	events := make([]*nostr.Event, 0, want)
	for len(events) < want {
		select {
		case event := <-incoming:
			if event.event != nil {
				events = append(events, event.event)
			}
		case <-deadline:
			t.Fatalf("received %d/%d relay events before timeout", len(events), want)
		}
	}
	return events
}

// TestServeRelayPingSurvivesBacklogWhileOwnerIsBlocked is the RED case for the
// inherited synchronous probe: the pong is written after more than frames' cap
// of valid EVENT frames while the owner is not draining, so only an async,
// backpressure-aware probe keeps the connection.
func TestServeRelayPingSurvivesBacklogWhileOwnerIsBlocked(t *testing.T) {
	const burst = 6
	peer := newRelayPingPeer(t, relayEventFrames(t, burst, "backlog"), true)
	c, _ := testClient(t)
	// One status event fits; the first data event then blocks the serve loop,
	// modelling an owner that is slower than the relay.
	incoming := make(chan relayEvent, 1)
	cancel, result, ticks := startRelayPingServe(t, c, peer, 300*time.Millisecond, incoming)
	defer stopRelayPingServe(t, cancel, result)
	<-peer.subscribed

	sendRelayPingTick(t, ticks, result)
	waitRelayPing(t, peer)

	// The reader is parked on the full queue now, so the pong cannot be read.
	// Hold the owner for well past the probe deadline.
	select {
	case <-result.done:
		t.Fatalf("live relay dropped while the owner was backlogged (ok=%v)", result.ok.Load())
	case <-time.After(900 * time.Millisecond):
	}

	events := waitRelayEvents(t, incoming, burst, 3*time.Second)
	for i, event := range events {
		if want := fmt.Sprintf("backlog-%d", i); event.Content != want {
			t.Fatalf("event %d content = %q, want %q", i, event.Content, want)
		}
	}

	// A frame the peer sends only after the probe deadline proves the reader is
	// still consuming the live socket, not just replaying its old queue.
	peer.writer <- relayEventFrames(t, 1, "sentinel")[0]
	sentinel := waitRelayEvents(t, incoming, 1, 3*time.Second)
	if sentinel[0].Content != "sentinel-0" {
		t.Fatalf("sentinel content = %q", sentinel[0].Content)
	}
	select {
	case <-result.done:
		t.Fatalf("serve loop exited after the backlog cleared (ok=%v)", result.ok.Load())
	default:
	}
}

// TestServeRelayPingCancellationJoinsInflightProbe cancels while a probe is
// waiting for a pong that will never arrive, and requires the serve loop, the
// reader and the probe goroutine to exit promptly.
func TestServeRelayPingCancellationJoinsInflightProbe(t *testing.T) {
	peer := newRelayPingPeer(t, nil, false)
	c, _ := testClient(t)
	cancel, result, ticks := startRelayPingServe(t, c, peer, time.Minute, nil)
	<-peer.subscribed

	sendRelayPingTick(t, ticks, result)
	waitRelayPing(t, peer)

	start := time.Now()
	cancel()
	select {
	case <-result.done:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not join the inflight probe and reader")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancellation took %v", elapsed)
	}
	select {
	case <-peer.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("peer did not observe the cancelled connection closing")
	}
}

// relayPingStub is a scripted relayConn: Read is idle unless a frame is queued, Ping
// parks until released, and CloseNow unblocks both. It gives deterministic
// control over probe overlap and probe outcomes.
type relayPingStub struct {
	pingN   atomic.Int32
	pinged  chan struct{}
	release chan struct{}
	frames  chan []byte
	closeCh chan struct{}
	once    sync.Once
}

func newRelayPingStub() *relayPingStub {
	return &relayPingStub{
		pinged:  make(chan struct{}, 8),
		release: make(chan struct{}),
		frames:  make(chan []byte, 8),
		closeCh: make(chan struct{}),
	}
}

func (s *relayPingStub) Read(ctx context.Context) (relayMessageType, []byte, error) {
	select {
	case frame := <-s.frames:
		return relayMessageText, frame, nil
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	case <-s.closeCh:
		return 0, nil, errors.New("relay closed")
	}
}

func (s *relayPingStub) Write(context.Context, relayMessageType, []byte) error { return nil }
func (s *relayPingStub) SetReadLimit(int64)                                    {}
func (s *relayPingStub) Ping(ctx context.Context) error {
	s.pingN.Add(1)
	select {
	case s.pinged <- struct{}{}:
	default:
	}
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.closeCh:
		return errors.New("relay closed")
	}
}
func (s *relayPingStub) CloseNow() error {
	s.once.Do(func() { close(s.closeCh) })
	return nil
}

func startRelayPingStubServe(t *testing.T, c *client, conn *relayPingStub, pongWait time.Duration) (context.CancelFunc, *relayPingServeResult, chan time.Time) {
	t.Helper()
	ctx, cancel := context.WithCancel(c.ctx)
	worker := &managedRelay{url: "stub", updates: make(chan relaySubscription, 1), publish: make(chan relayPublish, 1)}
	sub := relaySubscription{chats: []string{"lobby"}, since: nostr.Now()}
	result := &relayPingServeResult{done: make(chan struct{})}
	ticks := make(chan time.Time)
	go func() {
		_, ok := c.serveRelayWithPing(ctx, worker, conn, sub, 0, ticks, pongWait)
		result.ok.Store(ok)
		close(result.done)
	}()
	return cancel, result, ticks
}

// TestServeRelayPingSingleInflightAndSkipsBusyReads pins the bounds: repeated
// ticks during one probe start nothing new, and a tick after freshly read data
// skips the probe entirely.
func TestServeRelayPingSingleInflightAndSkipsBusyReads(t *testing.T) {
	c, _ := testClient(t)
	conn := newRelayPingStub()
	cancel, result, ticks := startRelayPingStubServe(t, c, conn, 5*time.Second)
	defer stopRelayPingServe(t, cancel, result)

	sendRelayPingTick(t, ticks, result)
	select {
	case <-conn.pinged:
	case <-time.After(time.Second):
		t.Fatal("idle connection did not probe")
	}
	for i := 0; i < 5; i++ {
		sendRelayPingTick(t, ticks, result)
	}
	if got := conn.pingN.Load(); got != 1 {
		t.Fatalf("inflight bounded = %d probes, want 1", got)
	}
	close(conn.release) // let the first probe complete successfully

	// Deliver a valid EVENT; once the serve loop hands it to the owner, the
	// reader revision is ahead of the last tick, so the next tick skips.
	conn.frames <- relayEventFrames(t, 1, "busy")[0]
	waitRelayEvents(t, c.incoming, 1, time.Second)
	sendRelayPingTick(t, ticks, result)
	time.Sleep(50 * time.Millisecond)
	if got := conn.pingN.Load(); got != 1 {
		t.Fatalf("probed despite recent read activity: %d probes", got)
	}

	// No new data: the next tick must probe again.
	sendRelayPingTick(t, ticks, result)
	select {
	case <-conn.pinged:
	case <-time.After(time.Second):
		t.Fatal("idle connection stopped probing after the busy tick")
	}
}

// TestServeRelayPingDeadWhenIdleAndUnanswered is the negative control: an
// otherwise idle relay that never answers the ping is still disconnected.
func TestServeRelayPingDeadWhenIdleAndUnanswered(t *testing.T) {
	c, _ := testClient(t)
	conn := newRelayPingStub()
	cancel, result, ticks := startRelayPingStubServe(t, c, conn, 50*time.Millisecond)
	defer cancel()

	sendRelayPingTick(t, ticks, result)
	select {
	case <-result.done:
	case <-time.After(2 * time.Second):
		t.Fatal("idle unanswered relay was not retired")
	}
	if result.ok.Load() {
		t.Fatal("dead relay reported a healthy serve result")
	}
}
