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
// View is the active view name captured when the action was created; Chat is an
// explicit default destination inside a group. Both are optional and empty for
// callers that act on the live active view only.
type UserAction struct {
	Type         string
	Payload      string
	View         string
	Chat         string
	ExpectedChat string // optional destination displayed by the composer
}

// MessageTarget is the resolved destination of a composed message. It echoes
// the inputs unchanged so a preview can be discarded when the composer moved
// on, and carries either the chat a send would publish to or the refusal.
type MessageTarget struct {
	Text        string
	View        string
	DefaultChat string
	Chat        string
	Error       string
}

// RelayInfo holds status information about a single relay connection.
type RelayInfo struct {
	URL       string
	Latency   time.Duration
	Connected bool
}

// DisplayEvent represents an event sent from the client to the TUI for display.
// INFO carries command output for Messages. STATUS carries normal operational
// information for Logs (displayed as INFO); WARN and ERROR are log severities.
// PRESENCE_UPDATE carries an immutable map[string]int of per-chat counts.
type DisplayEvent struct {
	Type         string
	Timestamp    string
	Nick         string
	Content      string
	FullPubKey   string
	ShortPubKey  string
	IsOwnMessage bool
	// RecipientUnchanged certifies that this validated message did not change
	// the owner's cached nickname, short identifier or chat for its author.
	// False is conservative for other producers and always refreshes previews.
	RecipientUnchanged bool
	RelayURL           string
	ID                 string
	Chat               string
	Payload            any
}

// StateUpdate is a specific payload for a DisplayEvent to update the TUI's state.
type StateUpdate struct {
	Views           []View
	ActiveViewIndex int
	ChatNicks       map[string]string // public display metadata, no signing material
}

type chatSession struct {
	privKey string
	pubKey  string
	nick    string
	// signer is the immutable parsed identity captured by queued publishes and
	// AUTH snapshots. It is derived once from privKey and validated against
	// pubKey; leave never zeroes it while a captured job still references it.
	signer *sessionSigner
}

// userContext holds cached information about a user in a specific chat.
type userContext struct {
	nick        string
	chat        string
	shortPubKey string
	// authorKey is a bounded, owner-only parsed verification key. It is only
	// stored after the event's ID and signature validated for this exact key.
	authorKey *authorPublicKey
}

// compiledPattern holds a pre-compiled regex or a literal string for matching.
type compiledPattern struct {
	raw     string
	regex   *regexp.Regexp
	literal string
}
