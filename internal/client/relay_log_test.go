package client

import (
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// relayLogEvents drains only log-level events, leaving transport snapshots out
// of health assertions.
func relayLogEvents(events chan DisplayEvent) []DisplayEvent {
	var out []DisplayEvent
	for {
		select {
		case event := <-events:
			if event.Type == "STATUS" || event.Type == "WARN" {
				out = append(out, event)
			}
		default:
			return out
		}
	}
}

func TestRelayHealthLogsTransitionsWithoutRedundantChurn(t *testing.T) {
	c, events := testClient(t)
	urls := []string{"ws://127.0.0.1:13001", "ws://127.0.0.1:13002"}
	c.config.AnchorRelays = urls
	c.config.Views = []View{{Name: "lobby"}}
	c.config.ActiveViewName = "lobby"
	seedRelaySelection(c, []string{"lobby"})

	// Nothing has connected yet: one initial notice, never a warning.
	c.updateAllSubscriptions()
	got := relayLogEvents(events)
	if len(got) != 1 || got[0].Type != "STATUS" || !strings.Contains(got[0].Content, "Connecting") {
		t.Fatalf("initial dialing events = %+v, want one STATUS connecting", got)
	}

	for _, worker := range c.relays {
		worker.connected = true
		worker.connectedAt = time.Now()
	}
	c.updateAllSubscriptions()
	got = relayLogEvents(events)
	if len(got) != 1 || got[0].Type != "STATUS" || !strings.Contains(got[0].Content, "Connected") || !strings.Contains(got[0].Content, "relay connection") {
		t.Fatalf("full coverage events = %+v, want one honest STATUS", got)
	}

	// Latency updates are live-view data, not log lines.
	for _, worker := range c.relays {
		worker.latency = 37 * time.Millisecond
	}
	c.updateAllSubscriptions()
	if got := relayLogEvents(events); len(got) != 0 {
		t.Fatalf("latency update logged %+v", got)
	}

	// A redundant relay above full coverage may join or leave without noise.
	redundant := "ws://127.0.0.1:13003"
	c.config.AnchorRelays = append(c.config.AnchorRelays, redundant)
	c.relays[redundant] = &managedRelay{
		url: redundant, cancel: func() {}, updates: make(chan relaySubscription, 1),
		chats: []string{"lobby"}, keys: c.subscriptionForRelay(redundant, []string{"lobby"}).keys,
		connected: true, connectedAt: time.Now(),
	}
	c.updateAllSubscriptions()
	if got := relayLogEvents(events); len(got) != 0 {
		t.Fatalf("redundant relay join logged %+v", got)
	}
	c.relayRetries.Add(redundant, relayRetry{until: time.Now().Add(time.Minute), delay: time.Minute})
	c.relays[redundant].connected = false
	c.updateAllSubscriptions()
	if got := relayLogEvents(events); len(got) != 0 {
		t.Fatalf("redundant relay loss logged %+v", got)
	}

	// Losing every route is one warning explaining the ongoing automatic retry.
	for _, url := range urls {
		c.relayRetries.Add(url, relayRetry{until: time.Now().Add(time.Minute), delay: time.Minute})
		c.relays[url].connected = false
	}
	c.updateAllSubscriptions()
	got = relayLogEvents(events)
	if len(got) != 1 || got[0].Type != "WARN" || !strings.Contains(got[0].Content, "retrying automatically") {
		t.Fatalf("outage events = %+v, want one cooldown WARN", got)
	}

	// Repeated cooldown wakes and reconnect attempts in the same outage must
	// stay silent; they are not a new health transition.
	for range 3 {
		c.updateAllSubscriptions()
		if got := relayLogEvents(events); len(got) != 0 {
			t.Fatalf("same-outage retry logged %+v", got)
		}
	}

	// Recovery is announced once, and redundant reconnects stay silent.
	for i, url := range urls {
		c.relayRetries.Add(url, relayRetry{delay: time.Minute})
		c.relays[url] = &managedRelay{
			url: url, cancel: func() {}, updates: make(chan relaySubscription, 1),
			chats: []string{"lobby"}, keys: c.subscriptionForRelay(url, []string{"lobby"}).keys,
			connected: true, connectedAt: time.Now(),
		}
		c.updateAllSubscriptions()
		got = relayLogEvents(events)
		if i == 0 {
			if len(got) != 1 || got[0].Type != "STATUS" || !strings.Contains(got[0].Content, "Recovered") {
				t.Fatalf("recovery events = %+v, want one STATUS recovery", got)
			}
			continue
		}
		if len(got) != 0 {
			t.Fatalf("redundant recovery logged %+v", got)
		}
	}

	c.config.Views, c.config.ActiveViewName = nil, ""
	c.updateAllSubscriptions()
	got = relayLogEvents(events)
	if len(got) != 1 || got[0].Type != "STATUS" || !strings.Contains(got[0].Content, "No active chat") {
		t.Fatalf("idle events = %+v, want one STATUS idle notice", got)
	}
}

func TestRelayHealthGroupPartialCoverageWarnsOnlyOnTransition(t *testing.T) {
	c, events := testClient(t)
	alpha, beta := "ws://127.0.0.1:13011", "ws://127.0.0.1:13012"
	c.config.AnchorRelays = []string{beta}
	c.relays[alpha] = &managedRelay{url: alpha, connected: true, chats: []string{"alpha"}}
	c.relays[beta] = &managedRelay{url: beta, connected: false, chats: []string{"beta"}}
	chats := []string{"alpha", "beta"}

	// Partial but still-dialing coverage during the initial group exchange is
	// normal and must not be a warning.
	c.logRelayHealth("group", chats)
	got := relayLogEvents(events)
	if len(got) != 1 || got[0].Type != "STATUS" || !strings.Contains(got[0].Content, "Connecting") {
		t.Fatalf("initial group events = %+v, want STATUS connecting", got)
	}

	// A failed route turns the same coverage into one degraded warning.
	c.relayRetries.Add(beta, relayRetry{until: time.Now().Add(time.Minute), delay: time.Minute})
	c.logRelayHealth("group", chats)
	got = relayLogEvents(events)
	if len(got) != 1 || got[0].Type != "WARN" || !strings.Contains(got[0].Content, "Partial") {
		t.Fatalf("degraded group events = %+v, want one partial WARN", got)
	}
	c.logRelayHealth("group", chats)
	if got := relayLogEvents(events); len(got) != 0 {
		t.Fatalf("unchanged partial coverage logged %+v", got)
	}

	// Switching views announces the state of the new view even when its class
	// matches the previous view.
	c.logRelayHealth("other", chats)
	got = relayLogEvents(events)
	if len(got) != 1 || got[0].Type != "WARN" || !strings.Contains(got[0].Content, "Partial") {
		t.Fatalf("view-switch events = %+v, want one partial WARN", got)
	}

	c.relays[beta].connected = true
	c.logRelayHealth("other", chats)
	got = relayLogEvents(events)
	if len(got) != 1 || got[0].Type != "STATUS" || !strings.Contains(got[0].Content, "Recovered") {
		t.Fatalf("group recovery events = %+v, want one STATUS recovery", got)
	}
}

func TestComputeRelayHealthWaitsForAllRoutesToFail(t *testing.T) {
	failed := map[string]bool{"ws://a": true}
	in := relayHealthInput{
		chats: []string{"chat"},
		relays: []relayHealthRelay{
			{url: "ws://a", chats: []string{"chat"}},
			{url: "ws://b", chats: []string{"chat"}},
		},
		pool: func(string) []string { return []string{"ws://a", "ws://b"} },
		failed: func(url string) bool {
			return failed[url]
		},
	}
	if report := computeRelayHealth(in); report.class != relayHealthConnecting || report.degraded {
		t.Fatalf("one failed route among dialing candidates classified as %v degraded=%v, want connecting", report.class, report.degraded)
	}
	failed["ws://b"] = true
	if report := computeRelayHealth(in); report.class != relayHealthOffline || !report.degraded {
		t.Fatalf("all failed routes classified as %v degraded=%v, want offline", report.class, report.degraded)
	}
}

func TestRelayProblemLoggingDeduplicatesWithinOutage(t *testing.T) {
	c, events := testClient(t)
	url := "ws://127.0.0.1:13021"
	c.config.AnchorRelays = []string{url}
	worker := &managedRelay{url: url}
	c.relays[url] = worker
	restricted := "relay access restricted: restricted: members only"

	c.logRelayProblem(worker, restricted)
	c.logRelayProblem(worker, restricted)
	got := relayLogEvents(events)
	if len(got) != 1 || got[0].Type != "WARN" || !strings.Contains(got[0].Content, url) || !strings.Contains(got[0].Content, "restricted:") {
		t.Fatalf("duplicate problem events = %+v, want one endpoint/reason WARN", got)
	}
	if count := countDrainedEvents(events, "ERROR"); count != 0 {
		t.Fatalf("problem logging emitted %d ERROR events", count)
	}
	// Cooldown retires handles. The same failure on a replacement worker is
	// still the same outage, not a new warning.
	worker = &managedRelay{url: url}
	c.relays[url] = worker
	c.logRelayProblem(worker, restricted)
	if got := relayLogEvents(events); len(got) != 0 {
		t.Fatalf("replacement worker repeated an existing warning: %+v", got)
	}

	// A different failure is new information for the same outage.
	c.logRelayProblem(worker, "relay authentication timed out")
	got = relayLogEvents(events)
	if len(got) != 1 || got[0].Type != "WARN" {
		t.Fatalf("changed problem events = %+v, want one WARN", got)
	}

	// A stable connection ends the outage; the same failure afterwards is a new
	// outage and must be visible again.
	c.relayRetries.Add(url, relayRetry{delay: time.Minute})
	worker.connected, worker.connectedAt = true, time.Now().Add(-2*c.relayTiming.stable)
	c.selectRelaySubscriptions([]string{"lobby"}, time.Now())
	if worker.lastProblem != "" || c.relayRetries.Contains(url) {
		t.Fatal("stable recovery did not clear problem/retry state")
	}
	c.logRelayProblem(worker, restricted)
	got = relayLogEvents(events)
	if len(got) != 1 || got[0].Type != "WARN" || !strings.Contains(got[0].Content, restricted) {
		t.Fatalf("post-recovery problem events = %+v, want one WARN", got)
	}
}

// countDrainedEvents is local to the log tests: relayHarness owns the Run-loop
// variant, while these tests drive owner methods directly.
func countDrainedEvents(events chan DisplayEvent, want string) int {
	n := 0
	for {
		select {
		case event := <-events:
			if event.Type == want {
				n++
			}
		default:
			return n
		}
	}
}

func TestRelayAuthRejectionWarnsOnceWithoutError(t *testing.T) {
	ts := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, _ int) {
		if !relayTestWaitREQ(ts, conn) {
			return
		}
		challenge := "reject-challenge"
		if !relayWriteJSON(ts, conn, []any{"AUTH", challenge}) ||
			!relayWriteJSON(ts, conn, relayClosedFrame("chat", "auth-required: proof required")) {
			return
		}
		frame, ok := relayNextFrame(ts, conn)
		if !ok {
			return
		}
		proof, ok := checkAuthProof(ts, frame, challenge)
		if !ok {
			return
		}
		// The AUTH rejection is followed by the serve loop's own failAuth
		// report. Both describe the same failure and must collapse to one line.
		if !relayWriteJSON(ts, conn, relayOKFrame(proof.ID, false, "restricted: members only")) {
			return
		}
		relayReadUntilClosed(ts, conn)
	})

	h := startRelayHarness(t, relayTestConfig(t, ts.url), nil)
	h.waitForEvent("restricted relay warning", func(event DisplayEvent) bool {
		return event.Type == "WARN" && strings.Contains(event.Content, ts.url) && strings.Contains(event.Content, "restricted:")
	})
	ts.waitClosed(1, transportWaitSlow)
	time.Sleep(250 * time.Millisecond)

	warnings := 0
	for _, event := range h.snapshot() {
		if event.Type == "WARN" && strings.Contains(event.Content, ts.url) {
			warnings++
		}
	}
	if warnings != 1 {
		t.Fatalf("authentication rejection produced %d warnings, want 1: %s", warnings, h.dump(10))
	}
	if got := h.countType("ERROR"); got != 0 {
		t.Fatalf("authentication rejection produced %d ERROR events, want 0", got)
	}
	h.stopWithin(time.Second)
}

func TestPlannerNoticeLevels(t *testing.T) {
	healthy := relayHealthReport{class: relayHealthHealthy}
	degraded := relayHealthReport{class: relayHealthOffline, degraded: true}
	for _, tc := range []struct {
		name     string
		notice   string
		health   relayHealthReport
		wantType string
		wantLog  bool
	}{
		{"safety limit", "Warning: the 256-relay safety limit reduces chat coverage.", healthy, "WARN", true},
		{"exhausted reserve", "Warning: geographic relay coverage is incomplete; reserve exhausted or cooling down.", healthy, "WARN", true},
		{"reserve transition", "Trying geographic reserve relays; primary recovery continues.", healthy, "STATUS", true},
		{"no eligible covered by health", "Warning: no eligible relay for an active chat; waiting for retry cooldown.", degraded, "", false},
		{"no eligible without health evidence", "Warning: no eligible relay for an active chat; waiting for retry cooldown.", healthy, "WARN", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event, log := plannerLogEvent(tc.notice, tc.health)
			if log != tc.wantLog {
				t.Fatalf("log = %v, want %v", log, tc.wantLog)
			}
			if log && event.Type != tc.wantType {
				t.Fatalf("type = %q, want %q", event.Type, tc.wantType)
			}
		})
	}
}
