package client

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/nbd-wtf/go-nostr"
)

func TestGeoFailoverAdvancesPastFailedReserve(t *testing.T) {
	var refusals atomic.Int32
	failed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refusals.Add(1)
		http.Error(w, "test refusal", http.StatusServiceUnavailable)
	}))
	t.Cleanup(failed.Close)
	live := func() *relayTestServer {
		return newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, _ int) {
			relayServeAckFrames(ts, conn, true, "")
		})
	}
	common, reserve := live(), live()
	chat, unavailable := "u4pruy", "ws://127.0.0.1:1"
	list := []string{unavailable}
	for rank := range 4 {
		list = append(list, fmt.Sprintf("%s/%d", common.url, rank))
	}
	list = append(list, "ws"+strings.TrimPrefix(failed.URL, "http"), reserve.url)
	cfg := relayTestConfig(t)
	cfg.Views, cfg.ActiveViewName = []View{{Name: chat}}, chat
	h := startRelayHarness(t, cfg, func(c *client) {
		c.geoPlans[chat] = [][]string{list}
		c.relayRetries.Add(unavailable, relayRetry{until: time.Now().Add(time.Minute), delay: time.Minute})
		c.relayTiming.minimum, c.relayTiming.maximum = 30*time.Second, 30*time.Second
		c.relayTiming.dialGap = time.Millisecond
	})
	h.waitRelayStatusCount(reserve.url, true, 1)
	if refusals.Load() != 1 {
		t.Fatalf("failed reserve attempts=%d, want one attempt before cooldown", refusals.Load())
	}
	h.send(UserAction{Type: "SEND_MESSAGE", Payload: "past the failed reserve"})
	assertGeoPublishedEvent(t, reserve, "past the failed reserve", chat)
	h.stop()
}

func TestRelayRetrySnapshotsGrowToDefaultCeiling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "test refusal", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	url := "ws" + strings.TrimPrefix(server.URL, "http")
	c := transportClient(t, relayTestConfig(t, url))
	c.relayTiming.dialGap = 0
	if c.relayTiming.maximum != 5*time.Minute || c.relayTiming.stable != time.Minute {
		t.Fatal("default retry ceiling or stability interval changed")
	}
	for _, previous := range []time.Duration{0, time.Second, 4 * time.Minute, 5 * time.Minute} {
		c.relayRetries.Add(url, relayRetry{delay: previous})
		start := time.Now()
		c.updateRelaySubscriptions(map[string][]string{url: {"lobby"}})
		worker := c.relays[url]
		var report relayEvent
		for report.worker != worker {
			select {
			case report = <-c.incoming:
			case <-time.After(transportWaitOp):
				t.Fatal("failed attempt did not report its next retry")
			}
		}
		window := max(c.relayTiming.minimum, previous)
		want := min(window*2, c.relayTiming.maximum)
		if report.connected || report.retry.delay != want {
			t.Fatalf("previous=%s next=%s, want %s", previous, report.retry.delay, want)
		}
		if report.retry.until.Before(start.Add(window/2)) || report.retry.until.After(time.Now().Add(window)) {
			t.Fatalf("retry deadline is outside its jittered %s window", window)
		}
		if report.retry.since < nostr.Now()-30 {
			t.Fatal("retry watermark lost its bounded reconnect overlap")
		}
		c.updateRelaySubscriptions(nil) // Cancel the timer instead of waiting minutes.
	}
}
