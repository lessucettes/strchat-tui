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
	geochatKind          = 20000
	namedChatKind        = 23333
	seenCacheSize        = 8192
	userContextCacheSize = 4096
	MaxMsgLen            = 2000
	maxChatNameLen       = 12
	orderingFlushDelay   = 200 * time.Millisecond
	perStreamBufferMax   = 256
)

const (
	maxDiscoveryDepth    = 2
	maxActiveDiscoveries = 10
	discoveryKind        = 10002
	connectTimeout       = 10 * time.Second
	verifyTimeout        = 5 * time.Second
	relayAddRateLimit    = 1000 * time.Millisecond
	debounceDelay        = 60 * time.Second
)

// defaultNamedChatRelays provides a fallback list of relays for named chats.
var defaultNamedChatRelays = []string{
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
}

type ChatSession struct {
	PrivKey    string
	PubKey     string
	Nick       string
	CustomNick bool
}

// userContext holds cached information about a user in a specific chat.
type userContext struct {
	nick        string
	chat        string
	shortPubKey string
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
