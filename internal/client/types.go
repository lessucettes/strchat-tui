package client

import (
	"regexp"
	"time"
)

// Constants for the client's operation.
const (
	geoChatKind          = 20000
	ephChatKind          = 23333
	seenCacheSize        = 8192
	userContextCacheSize = 4096
	MaxMsgLen            = 2000
	maxChatNameLen       = 12

	// defaultRelayCount is how many nearest relays are taken from each bundled
	// catalog for a geohash, so a pool holds at most twice this many (both
	// BitChat variants' nearest sets) before the relay cap applies.
	defaultRelayCount = 5

	// maxPoW bounds the settable and stored proof-of-work difficulty. Mining
	// shares one ten-second publish deadline (see executePublish). The real
	// publish path mines ~540 000 hashes/s on the development host, so 2^16
	// hashes take ~0.12 s there and ~6.5 s even at a hundredth of that rate. A
	// larger target would be accepted and then make every send in that chat time
	// out, and would also reject every peer that cannot mine it. 16 bits is 256x
	// BitChat's 8-bit target, far above what interoperability requires.
	maxPoW = 16
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

// StateUpdate is a specific payload for a DisplayEvent to update the TUI's state.
type StateUpdate struct {
	Views           []View
	ActiveViewIndex int
	Nick            string
}

type chatSession struct {
	privKey string
	pubKey  string
	nick    string
}

// userContext holds cached information about a user in a specific chat.
type userContext struct {
	nick        string
	chat        string
	shortPubKey string
}

// compiledPattern holds a pre-compiled regex or a literal string for matching.
type compiledPattern struct {
	raw     string
	regex   *regexp.Regexp
	literal string
}
