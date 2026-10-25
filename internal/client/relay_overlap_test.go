package client

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/nbd-wtf/go-nostr"
)

func TestGeoFailoverKeepsBoundedSubscriptionOverlap(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprintf("shared_reserve_%t", shared), func(t *testing.T) {
			fail := make(chan struct{})
			var once sync.Once
			trigger := func() { once.Do(func() { close(fail) }) }
			defer trigger()
			primary := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, _ int) {
				if !relayTestWaitREQ(ts, conn) {
					return
				}
				select {
				case <-fail:
					relayWriteJSON(ts, conn, relayClosedFrame("chat", "test outage"))
				case <-time.After(transportWaitSlow):
				}
			})
			live := func() *relayTestServer {
				return newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, _ int) {
					relayServeAckFrames(ts, conn, true, "")
				})
			}
			reserve, common := live(), live()
			var healthy []string
			for rank := range 4 {
				healthy = append(healthy, fmt.Sprintf("%s/%d", common.url, rank))
			}
			chat, other := "u4pruy", "9q8yyk"
			cfg := relayTestConfig(t)
			cfg.Views, cfg.ActiveViewName = []View{{Name: chat}}, chat
			if shared {
				cfg.Views = []View{{Name: "group", IsGroup: true, Children: []string{chat, other}}}
				cfg.ActiveViewName = "group"
			}
			h := startRelayHarness(t, cfg, func(c *client) {
				c.geoPlans[chat] = [][]string{append(append([]string{primary.url}, healthy...), reserve.url)}
				if shared {
					c.geoPlans[other] = [][]string{append([]string{reserve.url}, healthy...)}
				}
				c.relayTiming.minimum, c.relayTiming.maximum = time.Second, time.Second
				c.relayTiming.dialGap = time.Millisecond
			})
			h.waitRelayStatusCount(primary.url, true, 1)
			if shared {
				h.waitRelayStatusCount(reserve.url, true, 1)
			}
			before := nostr.Now()
			trigger()
			frame := reserve.waitREQContent(chat)
			since, ok := relayREQSince(t, frame)
			if !ok || since < before-30 || since > nostr.Now()-25 {
				t.Fatalf("failover since=%d, outage=%d: bounded recovery overlap was lost", since, before)
			}
			h.stop()
		})
	}
}

func TestRelaySubscriptionUpdateKeepsRetainedChatWatermark(t *testing.T) {
	for _, reconnecting := range []bool{false, true} {
		t.Run(fmt.Sprintf("reconnecting_%t", reconnecting), func(t *testing.T) {
			server := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, idx int) {
				if !relayTestWaitREQ(ts, conn) {
					return
				}
				if reconnecting && idx == 1 {
					return
				}
				relayServeAckFrames(ts, conn, true, "")
			})
			c := transportClient(t, relayTestConfig(t, server.url))
			c.relayTiming.minimum, c.relayTiming.maximum = 2*time.Second, 2*time.Second
			c.relayTiming.dialGap = 0
			worker := &managedRelay{url: server.url, updates: make(chan relaySubscription, 1), publish: make(chan relayPublish, 1)}
			now := nostr.Now()
			initial := relaySubscription{chats: []string{"lobby", "other"},
				since: map[string]nostr.Timestamp{"lobby": now - 10, "other": now - 10}}
			c.wg.Go(func() { c.runRelay(c.ctx, worker, initial) })
			server.takeREQ()
		waitRetry:
			for reconnecting {
				select {
				case report := <-c.incoming:
					if report.retry.delay > 0 {
						break waitRetry
					}
				case <-time.After(transportWaitOp):
					t.Fatal("worker did not enter reconnect cooldown")
				}
			}
			worker.updates <- relaySubscription{chats: []string{"fourth", "lobby"},
				since: map[string]nostr.Timestamp{"fourth": now, "lobby": now}}
			frame := server.takeREQ()
			var filters nostr.Filters
			for _, raw := range frame[2:] {
				var filter nostr.Filter
				if err := json.Unmarshal(raw, &filter); err != nil {
					t.Fatal(err)
				}
				filters = append(filters, filter)
			}
			// Filter-only probes: signatures are irrelevant to REQ matching.
			retained := &nostr.Event{Kind: ephChatKind, CreatedAt: now - 5, Tags: nostr.Tags{{"d", "lobby"}}}
			newcomer := &nostr.Event{Kind: ephChatKind, CreatedAt: now - 5, Tags: nostr.Tags{{"d", "fourth"}}}
			if !filters.Match(retained) || filters.Match(newcomer) {
				t.Fatalf("subscription update lost per-chat recovery boundaries: %s", mustMarshal(frame))
			}
		})
	}
}

func TestRelaySubscriptionClampsQueuedRecoveryOverlap(t *testing.T) {
	server := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, _ int) {
		if relayTestWaitREQ(ts, conn) {
			relayReadUntilClosed(ts, conn)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), transportWaitOp)
	defer cancel()
	conn, err := dialRelayTransport(ctx, server.url, nil, connectTimeout)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	before := nostr.Now()
	sub := relaySubscription{chats: []string{"u4pruy"}, since: map[string]nostr.Timestamp{"u4pruy": before - 120}}
	if err := subscribe(ctx, conn, sub); err != nil {
		t.Fatal(err)
	}
	since, ok := relayREQSince(t, server.takeREQ())
	if !ok || since < before-30 || since > nostr.Now() {
		t.Fatalf("stale queued watermark escaped the 30-second recovery window: %d", since)
	}
}
