// client/types.go
package client

import (
	"regexp"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

// Constants for the client's operation.
const (
	DefaultRelayCount    = 5
	GeochatKind          = 20000
	NamedChatKind        = 23333
	SeenCacheSize        = 8192
	UserContextCacheSize = 4096
	MaxMsgLen            = 2000
	MaxChatNameLen       = 12
)

// DefaultNamedChatRelays provides a fallback list of relays for named chats.
var DefaultNamedChatRelays = []string{
	"wss://relay.damus.io",
	"wss://relay.primal.net",
	"wss://nos.lol",
	"wss://offchain.pub",
	"wss://adre.su",
}

// UserAction represents an action initiated by the user from the TUI.
type UserAction struct {
	Type    string
	Payload string
}

// RelayInfo holds status information about a single relay connection.
type RelayInfo struct {
	URL     string
	Latency time.Duration
}

// DisplayEvent represents an event sent from the client to the TUI for display.
type DisplayEvent struct {
	Type      string
	Timestamp string
	Nick      string
	Color     string
	Content   string
	PubKey    string
	RelayURL  string
	ID        string
	Chat      string
	Payload   any
}

// StateUpdate is a specific payload for a DisplayEvent to update the TUI's state.
type StateUpdate struct {
	Views           []View
	ActiveViewIndex int
	Nick            string
}

// UserContext holds cached information about a user in a specific chat.
type UserContext struct {
	Nick    string
	Chat    string
	ShortPK string
}

// ManagedRelay wraps a nostr.Relay with additional state for management.
type ManagedRelay struct {
	URL          string
	Relay        *nostr.Relay
	Latency      time.Duration
	subscription *nostr.Subscription
	mu           sync.Mutex
}

// compiledPattern holds a pre-compiled regex or a literal string for matching.
type compiledPattern struct {
	raw     string
	regex   *regexp.Regexp
	literal string
}
