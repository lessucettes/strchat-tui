package client

import (
	"fmt"
	"slices"
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
			if event.Type == "STATUS" || event.Type == "WARN" || event.Type == "ERROR" {
				out = append(out, event)
			}
		default:
			return out
		}
	}
}

func TestGeoRetryPlanningDoesNotLogWithoutConnectivityChange(t *testing.T) {
	c, events := testClient(t)
	t.Cleanup(c.shutdown)
	chat, healthy := "u4pruy", "ws://127.0.0.1:13100"
	c.config.AnchorRelays = []string{healthy}
	c.config.Views = []View{{Name: chat}}
	c.config.ActiveViewName = chat
	list := make([]string, defaultRelayCount+1)
	for i := range list {
		list[i] = fmt.Sprintf("ws://127.0.0.1:%d", 13101+i)
	}
	c.geoPlans[chat] = [][]string{list, slices.Clone(list)}
	until := time.Now().Add(time.Hour)
	// The planner alternates exhausted, reserve, and normal selection while
	// the chat remains connected through an anchor. None is a user outage.
	for i, phase := range []string{"normal", "exhausted", "reserve", "normal", "exhausted", "reserve", "exhausted"} {
		for _, url := range append(slices.Clone(list), healthy) {
			if c.relays[url] == nil {
				c.relays[url] = &managedRelay{url: url, cancel: func() {}, updates: make(chan relaySubscription, 1)}
			}
			c.relayRetries.Remove(url)
		}
		c.relays[healthy].connected = true
		c.relays[healthy].connectedAt = time.Now()
		if phase != "normal" {
			for _, url := range list[:defaultRelayCount] {
				c.relayRetries.Add(url, relayRetry{until: until, delay: time.Minute})
			}
		}
		if phase == "exhausted" {
			c.relayRetries.Add(list[defaultRelayCount], relayRetry{until: until, delay: time.Minute})
		}
		c.updateAllSubscriptions()
		got := relayLogEvents(events)
		if i == 0 {
			if len(got) != 1 || got[0].Type != "STATUS" || !strings.Contains(got[0].Content, "Connected") {
				t.Fatalf("initial connection events = %+v", got)
			}
		} else if len(got) != 0 {
			t.Fatalf("planner phase %s logged without a connectivity change: %+v", phase, got)
		}
		for _, worker := range c.relays {
			if worker.done != nil {
				t.Fatal("selection-only regression started a real network worker")
			}
		}
	}
}

func TestRelayHealthDoesNotRestartConnectingAfterEstablishedCoverage(t *testing.T) {
	for _, loseAll := range []bool{false, true} {
		t.Run(fmt.Sprintf("lose_all_%t", loseAll), func(t *testing.T) {
			c, events := testClient(t)
			const group = "Group-240dec"
			chats := []string{"u4pruy", "9q8yyk"}
			urls := []string{"ws://127.0.0.1:13201", "ws://127.0.0.1:13202"}
			c.config.Views = []View{{Name: group, IsGroup: true, Children: chats}}
			c.config.ActiveViewName = group
			for i, chat := range chats {
				c.geoPlans[chat] = [][]string{{urls[i]}}
				c.relays[urls[i]] = &managedRelay{url: urls[i], chats: []string{chat}}
			}
			c.logRelayHealth(group, chats)
			for _, worker := range c.relays {
				worker.connected = true
			}
			c.logRelayHealth(group, chats)
			c.relays[urls[1]].connected = false
			if loseAll {
				c.relays[urls[0]].connected = false
			}
			// No retry failure snapshot yet: other attempts may still be in
			// flight, but this view has already established full coverage.
			for range 3 {
				c.logRelayHealth(group, chats)
			}
			for _, worker := range c.relays {
				worker.connected = true
			}
			c.logRelayHealth(group, chats)
			c.logRelayHealth(group, chats)
			got := relayLogEvents(events)
			want := []string{"STATUS", "STATUS", "WARN", "STATUS"}
			if len(got) != len(want) {
				t.Fatalf("connection/loss/recovery events = %+v, want exactly four transitions", got)
			}
			for i, kind := range want {
				if got[i].Type != kind {
					t.Fatalf("event %d = %+v, want %s", i, got[i], kind)
				}
			}
			loss := "Partial connectivity."
			if loseAll {
				loss = "No relay connection."
			}
			if !strings.Contains(got[2].Content, loss) {
				t.Fatalf("loss of coverage = %+v, want %s", got[2], loss)
			}
			if !strings.Contains(got[0].Content, "Connecting") || !strings.Contains(got[1].Content, "Connected") ||
				!strings.Contains(got[2].Content, fmt.Sprintf("%q", chats[1])) || !strings.Contains(got[3].Content, "restored") {
				t.Fatalf("transitions lost the initial connection, affected chat or recovery: %+v", got)
			}
			if loseAll && !strings.Contains(got[2].Content, fmt.Sprintf("%q", chats[0])) {
				t.Fatalf("complete outage omitted an affected chat: %+v", got[2])
			}

			// A different view still has its own initial connection phase.
			c.config.Views = append(c.config.Views, View{Name: "other", IsGroup: true, Children: chats})
			c.config.ActiveViewName = "other"
			for _, worker := range c.relays {
				worker.connected = false
			}
			c.logRelayHealth("other", chats)
			got = relayLogEvents(events)
			if len(got) != 1 || got[0].Type != "STATUS" || !strings.Contains(got[0].Content, "Connecting") {
				t.Fatalf("a new view inherited the previous view's outage: %+v", got)
			}
		})
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
	if len(got) != 1 || got[0].Type != "STATUS" || !strings.Contains(got[0].Content, "Connected") || !strings.HasPrefix(got[0].Content, `Chat "lobby": `) {
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
	if len(got) != 1 || got[0].Type != "WARN" || !strings.Contains(got[0].Content, "Retrying automatically") {
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
			if len(got) != 1 || got[0].Type != "STATUS" || !strings.Contains(got[0].Content, "restored") {
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
	c.config.Views = []View{{Name: "group", IsGroup: true, Children: []string{"alpha", "beta"}}}
	c.config.ActiveViewName = "group"
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
	if len(got) != 1 || got[0].Type != "WARN" || !strings.Contains(got[0].Content, `"beta"`) || !strings.HasPrefix(got[0].Content, `Group "group": Partial`) {
		t.Fatalf("degraded group events = %+v, want one partial WARN", got)
	}
	c.logRelayHealth("group", chats)
	if got := relayLogEvents(events); len(got) != 0 {
		t.Fatalf("unchanged partial coverage logged %+v", got)
	}
	// Starting a fresh reserve attempt does not end the existing outage.
	reserve := "ws://127.0.0.1:13014"
	c.relays[reserve] = &managedRelay{url: reserve, chats: []string{"beta"}}
	c.logRelayHealth("group", chats)
	if got := relayLogEvents(events); len(got) != 0 {
		t.Fatalf("a dialing reserve falsely changed the outage state: %+v", got)
	}
	// Another chat losing coverage is new information even though the overall
	// class stays partial. Name the affected chats instead of a stale count.
	gamma := "ws://127.0.0.1:13013"
	chats = append(chats, "gamma")
	c.config.Views[0].Children = slices.Clone(chats)
	c.relays[gamma] = &managedRelay{url: gamma, chats: []string{"gamma"}}
	c.relayRetries.Add(gamma, relayRetry{delay: time.Minute})
	c.logRelayHealth("group", chats)
	got = relayLogEvents(events)
	if len(got) != 1 || got[0].Type != "WARN" || !strings.Contains(got[0].Content, `"beta"`) || !strings.Contains(got[0].Content, `"gamma"`) {
		t.Fatalf("changed affected chats were not reported: %+v", got)
	}

	// Switching views announces the state of the new view even when its class
	// matches the previous view.
	c.logRelayHealth("other", chats)
	got = relayLogEvents(events)
	if len(got) != 1 || got[0].Type != "WARN" || !strings.Contains(got[0].Content, "Partial") {
		t.Fatalf("view-switch events = %+v, want one partial WARN", got)
	}

	c.relays[beta].connected = true
	c.relays[gamma].connected = true
	c.logRelayHealth("other", chats)
	got = relayLogEvents(events)
	if len(got) != 1 || got[0].Type != "STATUS" || !strings.Contains(got[0].Content, "restored") {
		t.Fatalf("group recovery events = %+v, want one STATUS recovery", got)
	}

	for _, url := range []string{alpha, beta, gamma} {
		c.relays[url].connected = false
		c.relayRetries.Add(url, relayRetry{delay: time.Minute})
	}
	c.logRelayHealth("group", chats)
	got = relayLogEvents(events)
	if len(got) != 1 || got[0].Type != "WARN" || !strings.Contains(got[0].Content, `"alpha", "beta", "gamma"`) {
		t.Fatalf("complete group outage did not name affected chats: %+v", got)
	}
	chats = chats[:2]
	c.config.Views[0].Children = slices.Clone(chats)
	c.logRelayHealth("group", chats)
	got = relayLogEvents(events)
	if len(got) != 1 || got[0].Type != "WARN" || !strings.Contains(got[0].Content, `"alpha", "beta"`) || strings.Contains(got[0].Content, `"gamma"`) {
		t.Fatalf("changed offline membership repeated a stale or uninformative summary: %+v", got)
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
		{"exhausted reserve", "Warning: geographic relay coverage is incomplete; reserve exhausted or cooling down.", healthy, "", false},
		{"reserve transition", "Trying geographic reserve relays; primary recovery continues.", healthy, "", false},
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
