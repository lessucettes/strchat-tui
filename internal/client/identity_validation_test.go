package client

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
)

// relayWireEvent is the strict JSON shape decodeRelayEvent must parse. It is
// deliberately independent of the upstream easyjson decoder, which ignores
// hex errors, accepts length mismatches and narrows an int kind to uint16.
type relayWireEvent struct {
	ID        string     `json:"id"`
	PubKey    string     `json:"pubkey"`
	CreatedAt int64      `json:"created_at"`
	Kind      int        `json:"kind"`
	Tags      nostr.Tags `json:"tags"`
	Content   string     `json:"content"`
	Sig       string     `json:"sig"`
}

func wireEventFor(ev *nostr.Event) relayWireEvent {
	return relayWireEvent{
		ID:        ev.ID.Hex(),
		PubKey:    ev.PubKey.Hex(),
		CreatedAt: int64(ev.CreatedAt),
		Kind:      int(ev.Kind),
		Tags:      ev.Tags,
		Content:   ev.Content,
		Sig:       hex.EncodeToString(ev.Sig[:]),
	}
}

// TestDecodeRelayEventEnforcesCanonicalWireIdentity exercises the actual JSON
// boundary. A typed nostr.Event cannot hold an uppercase or short identifier,
// so those malformed inputs are only meaningful before decoding. The uppercase
// public key is the regression case: it used to split an author's identity and
// bypass the block list's canonical lowercase lookup.
func TestDecodeRelayEventEnforcesCanonicalWireIdentity(t *testing.T) {
	event := signedEvent(t, "blocked sender")
	base := wireEventFor(event)
	cases := []struct {
		name     string
		edit     func(*relayWireEvent)
		accepted bool
	}{
		{"valid-lowercase", func(*relayWireEvent) {}, true},
		{"uppercase-signature", func(w *relayWireEvent) { w.Sig = strings.ToUpper(w.Sig) }, true},
		{"uppercase-pubkey", func(w *relayWireEvent) { w.PubKey = strings.ToUpper(w.PubKey) }, false},
		{"uppercase-id", func(w *relayWireEvent) { w.ID = strings.ToUpper(w.ID) }, false},
		{"short-pubkey", func(w *relayWireEvent) { w.PubKey = w.PubKey[:62] }, false},
		{"nonhex-pubkey", func(w *relayWireEvent) { w.PubKey = strings.Repeat("z", 64) }, false},
		{"short-id", func(w *relayWireEvent) { w.ID = w.ID[:62] }, false},
		{"nonhex-id", func(w *relayWireEvent) { w.ID = strings.Repeat("g", 64) }, false},
		{"short-sig", func(w *relayWireEvent) { w.Sig = w.Sig[:126] }, false},
		{"nonhex-sig", func(w *relayWireEvent) { w.Sig = strings.Repeat("g", 128) }, false},
		{"kind-overflow", func(w *relayWireEvent) { w.Kind = 1 << 16 }, false},
		{"kind-negative", func(w *relayWireEvent) { w.Kind = -1 }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wire := base
			tc.edit(&wire)
			data, err := json.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := decodeRelayEvent(data)
			if !tc.accepted {
				if err == nil {
					t.Fatalf("malformed wire event decoded as %+v", decoded)
				}
				return
			}
			if err != nil {
				t.Fatalf("valid wire event rejected: %v", err)
			}
			if decoded == nil {
				t.Fatal("decoder returned a nil event without an error")
			}
			if tc.name == "uppercase-signature" {
				if decoded.Sig != event.Sig || !decoded.CheckID() || !decoded.VerifySignature() {
					t.Fatal("uppercase signature did not decode to the same valid bytes")
				}
			}
			if tc.name == "valid-lowercase" {
				if decoded.ID != event.ID || decoded.PubKey != event.PubKey || decoded.Sig != event.Sig {
					t.Fatal("valid wire event changed identity or signature bytes")
				}
				c, events := testClient(t)
				c.config.BlockedUsers = []blockedUser{{PubKey: decoded.PubKey.Hex()}}
				c.processEvent(decoded, "local")
				if len(events) != 0 {
					t.Fatal("blocked author was displayed after decoding")
				}
				c.config.BlockedUsers = nil
				c.processEvent(decoded, "local")
				if len(events) != 1 {
					t.Fatal("unblocked valid event was rejected or poisoned deduplication")
				}
			}
		})
	}
}
