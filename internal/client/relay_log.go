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
	missing  []string
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
	for chat := range active {
		if _, ok := covered[chat]; !ok {
			report.missing = append(report.missing, chat)
		}
	}
	slices.Sort(report.missing)
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
// view, class and diagnostic detail, not per-URL or per-view history.
type relayHealthLog struct {
	initialized bool
	view        string
	class       relayHealthClass
	missing     string
	cause       string
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
			return "STATUS", "Relay connections restored for all active chats."
		}
		return "STATUS", "Connected to relays for all active chats."
	case relayHealthPartial, relayHealthOffline:
		if report.class == relayHealthOffline && report.total == 1 {
			return "WARN", "No relay connection. Retrying automatically."
		}
		names := make([]string, 0, min(3, len(report.missing)))
		for _, chat := range report.missing[:min(3, len(report.missing))] {
			names = append(names, fmt.Sprintf("%q", chat))
		}
		affected := strings.Join(names, ", ")
		if len(report.missing) > len(names) {
			affected += fmt.Sprintf(" and %d more", len(report.missing)-len(names))
		}
		status := "Partial connectivity."
		if report.class == relayHealthOffline {
			status = "No relay connection."
		}
		return "WARN", status + " Chats without a relay connection: " + affected + ". Retrying automatically."
	default:
		return "STATUS", "Connecting to relays."
	}
}

// logRelayHealth reports a changed view, connection state, affected chat set or
// diagnostic example. It never touches sockets or widgets.
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
	if report.class == relayHealthConnecting && c.relayHealth.view == viewName &&
		(c.relayHealth.class == relayHealthHealthy || c.relayHealth.class == relayHealthOffline || c.relayHealth.class == relayHealthPartial) {
		// Once coverage has been established or an outage reported, a missing
		// route is degradation, not a new initial connection phase. A fresh
		// attempt alone neither restores coverage nor ends an outage.
		report.degraded = true
		report.class = relayHealthOffline
		if report.covered > 0 {
			report.class = relayHealthPartial
		}
	}
	emit, previous, switched := c.relayHealth.transition(viewName, report.class)
	stateChanged := emit
	missing, cause := "", ""
	affectedChanged, causeChanged := false, false
	if report.degraded {
		cause = c.relayFailureExample(report.missing)
		missing = strings.Join(report.missing, "\x00")
		affectedChanged = missing != c.relayHealth.missing
		causeChanged = cause != "" && cause != c.relayHealth.cause
		// A newly affected chat or a changed known cause is useful even if
		// the broad class stays partial/offline. Identical retries are not.
		emit = emit || affectedChanged || causeChanged
	}
	c.relayHealth.missing, c.relayHealth.cause = missing, cause
	if emit {
		eventType, content := relayHealthMessage(report, previous, switched)
		if !stateChanged && !affectedChanged && causeChanged {
			content = "Connection failure changed. " + cause
		} else if cause != "" {
			content += " " + cause
		}
		if viewName != "" {
			content = c.viewLogScope(viewName) + content
		}
		c.emit(DisplayEvent{Type: eventType, Content: content})
	}
	return report
}

// Use one deterministic, relevant example instead of dumping every failed
// address. The existing retry cache retains its bounded cause across workers.
func (c *client) relayFailureExample(chats []string) string {
	for _, chat := range chats {
		for _, url := range c.relayPlanFor(chat).pool {
			if worker := c.relays[url]; worker != nil && worker.connected {
				continue
			}
			retry, _ := c.relayRetries.Peek(url)
			if retry.failure != "" && retry.failure != retry.lastProblem {
				return fmt.Sprintf("Example failure at relay %s. Reason: %q.", url, retry.failure)
			}
		}
	}
	return ""
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
	c.emit(DisplayEvent{Type: "WARN", Content: fmt.Sprintf("Relay %s: Request failed. Reason: %q.", worker.url, problem)})
}

func (c *client) viewLogScope(viewName string) string {
	kind := "Chat"
	if view := c.getActiveView(); view != nil && view.Name == viewName && view.IsGroup {
		kind = "Group"
	}
	return fmt.Sprintf("%s %q: ", kind, truncateString(sanitizeString(viewName), 48))
}

// Planner churn is not a user-visible outage. Report only actionable limits
// or missing configuration not already explained by the connectivity summary.
func plannerLogEvent(notice string, health relayHealthReport) (DisplayEvent, bool) {
	if strings.HasPrefix(notice, "Warning: the ") {
		return DisplayEvent{Type: "WARN", Content: fmt.Sprintf("Relay connection limit (%d) reached. Chat coverage may be reduced. Use fewer relays or a smaller group.", maxActiveRelays)}, true
	}
	if strings.HasPrefix(notice, "Warning: no eligible relay") && !health.degraded {
		return DisplayEvent{Type: "WARN", Content: "No relay connection is available for an active chat. Check the relay settings; unavailable relays will be retried automatically."}, true
	}
	return DisplayEvent{}, false
}
