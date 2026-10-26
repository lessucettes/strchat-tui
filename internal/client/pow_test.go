package client

import (
	"fmt"
	"math/bits"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip13"
)

// powLeadingBits is the independent oracle for the digest fixtures below.
func powLeadingBits(digest [32]byte) int {
	n := 0
	for _, b := range digest {
		if b == 0 {
			n += 8
			continue
		}
		return n + bits.LeadingZeros8(b)
	}
	return n
}

func TestPoWValidationUsesLastNonceAndClaimedDifficulty(t *testing.T) {
	for _, minDifficulty := range []int{0, 1, 4, 8, 12, 16} {
		for claimed := 0; claimed <= 16; claimed++ {
			for leading := 0; leading <= 20; leading++ {
				var digest [32]byte
				// Set the first `leading` bits to zero and the next bit to one.
				if leading < 256 {
					digest[leading/8] |= 1 << (7 - leading%8)
				}
				ev := &nostr.Event{ID: nostr.ID(digest), Tags: nostr.Tags{{"nonce", "1", fmt.Sprint(claimed)}}}
				want := minDifficulty <= 0 || (claimed >= minDifficulty && powLeadingBits(digest) >= claimed)
				if got := isPoWValidDigest(ev, minDifficulty, digest); got != want {
					t.Fatalf("min=%d claimed=%d leading=%d: got %t want %t", minDifficulty, claimed, leading, got, want)
				}
			}
		}
	}
	// Admission parses the last nonce tag (mining appends it) and trims the
	// claim; short, missing and non-numeric tags cannot satisfy a target.
	var digest [32]byte // 256 leading zero bits
	cases := []struct {
		name string
		tags nostr.Tags
		min  int
		want bool
	}{
		{"missing", nil, 1, false},
		{"short", nostr.Tags{{"nonce", "1"}}, 1, false},
		{"non-numeric", nostr.Tags{{"nonce", "1", "x"}}, 1, false},
		{"trimmed", nostr.Tags{{"nonce", "1", "  4  "}}, 4, true},
		{"below-target", nostr.Tags{{"nonce", "1", "4"}}, 8, false},
		{"last-tag-wins", nostr.Tags{{"nonce", "1", "16"}, {"nonce", "2", "2"}}, 2, true},
		{"ignores-first-high-claim", nostr.Tags{{"nonce", "1", "16"}, {"nonce", "2", "2"}}, 8, false},
	}
	for _, tc := range cases {
		ev := &nostr.Event{ID: nostr.ID(digest), Tags: tc.tags}
		if got := isPoWValidDigest(ev, tc.min, digest); got != tc.want {
			t.Errorf("%s: got %t want %t", tc.name, got, tc.want)
		}
	}
}

func TestProcessEventUsesVerifiedDigestForPoW(t *testing.T) {
	c, events := testClient(t)
	c.config.Views[0].PoW = 1
	key := signerTestKeys[0]
	signer, err := newSessionSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	ev := nostr.Event{Kind: ephChatKind, CreatedAt: nostr.Now(), Content: "pow", Tags: nostr.Tags{{"d", "lobby"}, {"n", "alice"}, {"nonce", "0", "1"}}}
	for nonce := 0; ; nonce++ {
		ev.Tags[len(ev.Tags)-1][1] = fmt.Sprint(nonce)
		if err := signer.sign(&ev); err != nil {
			t.Fatal(err)
		}
		if nip13.Difficulty(ev.ID) >= 1 {
			break
		}
		if nonce > 1000 {
			t.Fatal("could not mine difficulty 1")
		}
	}
	c.processEvent(&ev, "local")
	if len(events) != 1 {
		t.Fatal("valid PoW event was rejected by the digest-based check")
	}
	<-events
	// Removing the proof while keeping the signature valid must reject it.
	bad := ev
	bad.Tags = nostr.Tags{{"d", "lobby"}, {"n", "alice"}}
	if err := signer.sign(&bad); err != nil {
		t.Fatal(err)
	}
	c.seenCache.Purge()
	c.processEvent(&bad, "local")
	if len(events) != 0 {
		t.Fatal("event without required PoW was accepted")
	}
}
