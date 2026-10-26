package client

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestGeoRelayRecoveryRejoinsPrimaryOnlyPeer(t *testing.T) {
	var available atomic.Bool
	primary := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, _ int) {
		if !available.Load() {
			if relayTestWaitREQ(ts, conn) {
				relayWriteJSON(ts, conn, []any{"CLOSED", "chat", "test outage"})
			}
			return
		}
		relayServeAckFrames(ts, conn, true, "")
	})
	newLive := func() *relayTestServer {
		return newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, _ int) {
			relayServeAckFrames(ts, conn, true, "")
		})
	}
	reserve := newLive()
	var common []string
	for range 4 {
		common = append(common, newLive().url)
	}
	chat := "u4pruy"
	cfg := relayTestConfig(t)
	cfg.Views = []View{{Name: chat}}
	cfg.ActiveViewName = chat
	h := startRelayHarness(t, cfg, func(c *client) {
		c.geoPlans[chat] = [][]string{append(append([]string{primary.url}, common...), reserve.url), common}
		c.relayTiming = relayTiming{minimum: 100 * time.Millisecond, maximum: 400 * time.Millisecond,
			stable: 50 * time.Millisecond, dialGap: time.Millisecond}
	})
	h.waitRelayStatusCount(reserve.url, true, 1)
	h.send(UserAction{Type: "SEND_MESSAGE", Payload: "through reserve"})
	assertGeoPublishedEvent(t, reserve, "through reserve", chat)

	available.Store(true)
	h.waitForEvent("recovered primary warming alongside reserve", func(event DisplayEvent) bool {
		primaryInfo, primaryPresent := relayInfoFor(event, primary.url)
		_, reservePresent := relayInfoFor(event, reserve.url)
		return primaryPresent && primaryInfo.Connected && reservePresent
	})
	// A healthy reserve must not suppress primary retries. Once the recovered
	// primary is stable, its unnecessary replacement must disappear.
	if !waitUntil(t, transportWaitOp, func() bool {
		statuses, _ := h.latestRelayStatuses()
		primaryReady, reservePresent := false, false
		for _, status := range statuses {
			if status.URL == primary.url {
				primaryReady = status.Connected
			}
			reservePresent = reservePresent || status.URL == reserve.url
		}
		return primaryReady && !reservePresent
	}) {
		t.Fatal("healthy reserve suppressed primary recovery or was not retired")
	}
	h.send(UserAction{Type: "SEND_MESSAGE", Payload: "back at primary-only peer"})
	assertGeoPublishedEvent(t, primary, "back at primary-only peer", chat)
	h.stop()
}

func assertGeoPublishedEvent(t *testing.T, relay *relayTestServer, content, chat string) {
	t.Helper()
	select {
	case event := <-relay.eventCh:
		if event.Content != content || event.Kind != geoChatKind || event.Tags.FindWithValue("g", chat) == nil {
			t.Fatalf("unexpected geo event: kind=%d content=%q tags=%v", event.Kind, event.Content, event.Tags)
		}
		if !event.VerifySignature() {
			t.Fatal("invalid geo signature")
		}
	case <-time.After(transportWaitOp):
		t.Fatalf("relay %s did not receive geo publication", relay.url)
	}
}
