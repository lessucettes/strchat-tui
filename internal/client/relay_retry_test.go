package client

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFastRelayRefusalsArePaced(t *testing.T) {
	started := make(chan time.Time, 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- time.Now()
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	c := transportClient(t, relayTestConfig(t))
	desired := make(map[string][]string)
	for i := range 16 {
		desired[fmt.Sprintf("ws%s/%d", strings.TrimPrefix(server.URL, "http"), i)] = []string{"lobby"}
	}
	c.updateRelaySubscriptions(desired)
	var first, last time.Time
	for i := range 8 {
		select {
		case at := <-started:
			if i == 0 {
				first = at
			}
			last = at
		case <-time.After(transportWaitOp):
			t.Fatal("paced dial queue did not make progress")
		}
	}
	if elapsed := last.Sub(first); elapsed < 600*time.Millisecond {
		t.Fatalf("eight fast refusals started in %s; parallel dial limit alone did not prevent a burst", elapsed)
	}
	c.Stop()
}

func TestNetworkFailureCooldownSurvivesViewSwitches(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	url := "ws" + strings.TrimPrefix(server.URL, "http")
	cfg := relayTestConfig(t, url)
	cfg.Views = []View{{Name: "lobby"}, {Name: "other"}}
	h := startRelayHarness(t, cfg, func(c *client) {
		c.relayTiming.minimum = 30 * time.Second
		c.relayTiming.maximum = 30 * time.Second
	})
	h.waitForEvent("cooldown after a real failed dial", contentPred("waiting for retry cooldown"))
	for range 3 {
		h.send(UserAction{Type: "ACTIVATE_VIEW", Payload: "other"})
		h.send(UserAction{Type: "ACTIVATE_VIEW", Payload: "lobby"})
	}
	time.Sleep(300 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("view changes retried a known unavailable URL %d times", got)
	}
	h.stop()
}
