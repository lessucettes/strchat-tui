package client

import (
	"fmt"
	"slices"
	"strings"
)

// Relay health is summarized from what the owner actually knows: a chat is
// covered only when a connected worker's subscription snapshot includes it.
// Relay counts, dial attempts and latency changes are deliberately not inputs;
// they belong to the live relay view.
type relayHealthClass uint8

const (
	relayHealthIdle relayHealthClass = iota
	relayHealthConnecting
	relayHealthHealthy
	relayHealthPartial
	relayHealthOffline
)

type relayHealthRelay struct {
	url       string
	connected bool
	chats     []string
}

type relayHealthInput struct {
	chats  []string
	relays []relayHealthRelay
	// pool returns the configured candidates for a chat, so a chat whose
	// relays have all retired into cooldown can still show failure evidence.
	pool func(string) []string
	// failed reports whether a URL is inside a failure/cooldown snapshot.
	failed func(string) bool
}

type relayHealthReport struct {
	class    relayHealthClass
	total    int
	covered  int
	degraded bool
}

// computeRelayHealth classifies a view without side effects. Missing coverage
// is only degraded when every known route for a missing chat is cooling down;
// normal initial dialing and a redundant relay failure are not warnings.
func computeRelayHealth(in relayHealthInput) relayHealthReport {
	report := relayHealthReport{class: relayHealthIdle, total: len(in.chats)}
	if len(in.chats) == 0 {
		return report
	}
	active := make(map[string]struct{}, len(in.chats))
	for _, chat := range in.chats {
		active[chat] = struct{}{}
	}
	covered := make(map[string]struct{}, len(in.chats))
	for _, relay := range in.relays {
		if !relay.connected {
			continue
		}
		for _, chat := range relay.chats {
			if _, ok := active[chat]; ok {
				covered[chat] = struct{}{}
			}
		}
	}
	report.covered = len(covered)
	if report.covered == report.total {
		report.class = relayHealthHealthy
		return report
	}
	if in.failed != nil {
		for chat := range active {
			if _, ok := covered[chat]; ok {
				continue
			}
			// A historical failure on one candidate does not prove an outage
			// while another candidate is still dialing. Warn only when every
			// known route for this chat is cooling down.
			allFailed, found := true, false
			if in.pool != nil {
				for _, url := range in.pool(chat) {
					found = true
					if !in.failed(url) {
						allFailed = false
						break
					}
				}
			}
			if allFailed {
				for _, relay := range in.relays {
					if !slices.Contains(relay.chats, chat) {
						continue
					}
					found = true
					if !in.failed(relay.url) {
						allFailed = false
						break
					}
				}
			}
			if found && allFailed {
				report.degraded = true
				break
			}
		}
	}
	report.class = relayHealthConnecting
	if !report.degraded {
		return report
	}
	if report.covered == 0 {
		report.class = relayHealthOffline
	} else {
		report.class = relayHealthPartial
	}
	return report
}

// relayHealthLog is the owner's bounded dedup state. It stores only the last
// view and class, so no per-URL or per-view history accumulates.
type relayHealthLog struct {
	initialized bool
	view        string
	class       relayHealthClass
}

func (l *relayHealthLog) transition(view string, class relayHealthClass) (emit bool, previous relayHealthClass, switched bool) {
	if !l.initialized {
		l.initialized = true
		l.view, l.class = view, class
		// Startup without any active view is already explained by Run's INFO
		// notice; an idle transition after a view is still announced.
		return view != "" || class != relayHealthIdle, relayHealthIdle, false
	}
	previous = l.class
	if view != l.view {
		l.view, l.class = view, class
		return true, previous, true
	}
	if class != l.class {
		l.class = class
		return true, previous, false
	}
	return false, previous, false
}

func relayHealthMessage(report relayHealthReport, previous relayHealthClass, switched bool) (string, string) {
	switch report.class {
	case relayHealthIdle:
		return "STATUS", "No active chat. Use /join to start chatting."
	case relayHealthHealthy:
		if !switched && (previous == relayHealthPartial || previous == relayHealthOffline) {
			return "STATUS", "Recovered: relay connections restored for all active chats."
		}
		return "STATUS", "Connected: all active chats have a relay connection."
	case relayHealthPartial:
		return "WARN", "Partial relay coverage: some active chats have no relay connection; retrying automatically."
	case relayHealthOffline:
		return "WARN", "No relay connection; retrying automatically."
	default:
		return "STATUS", "Connecting to relays."
	}
}

// logRelayHealth emits at most one line per view/class transition. It only
// reads owner state and writes an event; it never touches sockets or widgets.
func (c *client) logRelayHealth(viewName string, chats []string) relayHealthReport {
	relays := make([]relayHealthRelay, 0, len(c.relays))
	for _, worker := range c.relays {
		relays = append(relays, relayHealthRelay{url: worker.url, connected: worker.connected, chats: worker.chats})
	}
	report := computeRelayHealth(relayHealthInput{
		chats:  chats,
		relays: relays,
		pool:   func(chat string) []string { return c.relayPlanFor(chat).pool },
		failed: func(url string) bool {
			retry, ok := c.relayRetries.Peek(url)
			return ok && retry.delay > 0
		},
	})
	if emit, previous, switched := c.relayHealth.transition(viewName, report.class); emit {
		eventType, content := relayHealthMessage(report, previous, switched)
		if viewName != "" {
			content = truncateString(sanitizeString(viewName), 48) + ": " + content
		}
		c.emit(DisplayEvent{Type: eventType, Content: content})
	}
	return report
}

// logRelayProblem owns cross-reconnect dedup for relay-side auth/access
// failures. The last reported text is bounded by active relay handles and is
// cleared only by a stable connection in selectRelaySubscriptions, so one
// outage reports one endpoint/reason line without hiding a later outage.
func (c *client) logRelayProblem(worker *managedRelay, problem string) {
	if problem == "" {
		return
	}
	retry, _ := c.relayRetries.Peek(worker.url)
	if worker.lastProblem == problem || retry.lastProblem == problem {
		worker.lastProblem = problem
		return
	}
	worker.lastProblem = problem
	retry.lastProblem = problem
	c.relayRetries.Add(worker.url, retry)
	c.emit(DisplayEvent{Type: "WARN", Content: fmt.Sprintf("Relay %s: %s", worker.url, problem)})
}

// plannerLogEvent levels the selector's fixed notices. The no-eligible notice
// is redundant when the health summary already warned about the same outage;
// without failure evidence it remains the only warning for the user.
func plannerLogEvent(notice string, health relayHealthReport) (DisplayEvent, bool) {
	if notice == "" {
		return DisplayEvent{}, false
	}
	if strings.HasPrefix(notice, "Warning: no eligible relay") && health.degraded {
		return DisplayEvent{}, false
	}
	if strings.HasPrefix(notice, "Warning:") {
		return DisplayEvent{Type: "WARN", Content: strings.TrimPrefix(notice, "Warning: ")}, true
	}
	return DisplayEvent{Type: "STATUS", Content: notice}, true
}
