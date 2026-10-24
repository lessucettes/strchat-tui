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

func TestOutageReportsChangedCauseButNotRepeatedConnectionAttempts(t *testing.T) {
	var calls, status atomic.Int32
	status.Store(http.StatusServiceUnavailable)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "unavailable", int(status.Load()))
	}))
	t.Cleanup(server.Close)
	url := "ws" + strings.TrimPrefix(server.URL, "http")
	h := startRelayHarness(t, relayTestConfig(t, url), func(c *client) {
		c.relayTiming.minimum = 20 * time.Millisecond
		c.relayTiming.maximum = 40 * time.Millisecond
		c.relayTiming.dialGap = 0
	})
	h.waitForEvent("first outage cause", func(event DisplayEvent) bool {
		return event.Type == "WARN" && strings.Contains(event.Content, "HTTP status 503")
	})
	h.waitForEvent("repeated real connection attempts", func(DisplayEvent) bool { return calls.Load() >= 3 })
	h.assertNoEventsFrom(h.mark(), 100*time.Millisecond, "quiet retries with the same cause", func(event DisplayEvent) bool {
		return event.Type == "WARN" || event.Type == "ERROR" || event.Type == "STATUS"
	})
	status.Store(http.StatusForbidden)
	changed := h.waitForEvent("changed outage cause", func(event DisplayEvent) bool {
		return event.Type == "WARN" && strings.Contains(event.Content, "HTTP status 403") && strings.Contains(event.Content, url)
	})
	if !strings.Contains(changed.Content, "Connection failure changed.") || strings.Contains(changed.Content, "Retrying automatically") {
		t.Fatalf("changed cause repeated the initial outage boilerplate: %s", changed.Content)
	}
	h.assertNoEventsFrom(h.mark(), 100*time.Millisecond, "quiet retries after the changed cause", func(event DisplayEvent) bool {
		return event.Type == "WARN" || event.Type == "ERROR" || event.Type == "STATUS"
	})
	if got := h.countType("WARN"); got != 2 || h.countType("ERROR") != 0 {
		t.Fatalf("want one warning per distinct cause, got %d warnings: %s", got, h.dump(20))
	}
	h.stopWithin(time.Second)
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
	event := h.waitForEvent("cooldown after a real failed dial", contentPred("No relay connection"))
	if event.Type != "WARN" || !strings.Contains(event.Content, url) || !strings.Contains(event.Content, "503") || !strings.Contains(event.Content, "Reason:") {
		t.Fatalf("outage warning lost the failed endpoint and handshake cause: %s", event.Content)
	}
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
