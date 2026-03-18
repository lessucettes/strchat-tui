package client

import (
	"regexp"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

// Constants for the client's operation.
const (
	defaultRelayCount    = 5
	geoChatKind          = 20000
	ephChatKind          = 23333
	seenCacheSize        = 8192
	userContextCacheSize = 4096
	MaxMsgLen            = 2000
	maxChatNameLen       = 12
	orderingFlushDelay   = 200 * time.Millisecond
	perStreamBufferMax   = 256
	// How far back we ask relays for events to populate history on switch.
	messageHistoryLookback = 10 * time.Minute
	// Per-filter maximum number of stored events to request.
	// This prevents relays from sending an unbounded amount of history.
	messageHistoryLimit = 2000
)

// defaultEphChatRelays provides a fallback list of relays for named chats.
var defaultEphChatRelays = []string{
	"wss://relay.damus.io",
	"wss://relay.primal.net",
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
	URL       string
	Latency   time.Duration
	Connected bool
}

// DisplayEvent represents an event sent from the client to the TUI for display.
type DisplayEvent struct {
	Type         string
	Timestamp    string
	CreatedAt    int64
	Nick         string
	Content      string
	FullPubKey   string
	ShortPubKey  string
	IsOwnMessage bool
	RelayURL     string
	ID           string
	Chat         string
	Payload      any
}

// ChatUser represents a user known inside a chat (cached from received events).
// It is used to render the users list in the UI and to build private-message prefixes.
type ChatUser struct {
	PubKey       string
	Nick         string
	ShortPubKey  string
	Chat         string
	LastMsgAt    int64
}

type orderItem struct {
	ev        DisplayEvent
	createdAt int64
	id        string
}

// StateUpdate is a specific payload for a DisplayEvent to update the TUI's state.
type StateUpdate struct {
	Views           []View
	ActiveViewIndex int
	Nick            string
	ShortPubKey     string
}

type chatSession struct {
	privKey    string
	pubKey     string
	nick       string
	customNick bool
}

// userContext holds cached information about a user in a specific chat.
type userContext struct {
	nick        string
	chat        string
	shortPubKey string
	lastMsgAt   int64
}

// managedRelay wraps a nostr.Relay with additional state for management.
type managedRelay struct {
	url               string
	relay             *nostr.Relay
	latency           time.Duration
	subscription      *nostr.Subscription
	connected         bool
	reconnectAttempts int
	mu                sync.Mutex
}

// compiledPattern holds a pre-compiled regex or a literal string for matching.
type compiledPattern struct {
	raw     string
	regex   *regexp.Regexp
	literal string
}
