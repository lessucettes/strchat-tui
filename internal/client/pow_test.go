package client

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

func TestCountLeadingZeroBitsDigestMatchesHex(t *testing.T) {
	for range 2000 {
		var digest [32]byte
		if _, err := rand.Read(digest[:]); err != nil {
			t.Fatal(err)
		}
		want := countLeadingZeroBits(hex.EncodeToString(digest[:]))
		if got := countLeadingZeroBitsDigest(digest); got != want {
			t.Fatalf("digest %x: got %d want %d", digest, got, want)
		}
	}
	// Exercise every possible leading byte and a fully zero digest.
	var zero [32]byte
	if got := countLeadingZeroBitsDigest(zero); got != 256 {
		t.Fatalf("zero digest counted %d bits", got)
	}
	for b := range 256 {
		var d [32]byte
		d[0] = byte(b)
		if got, want := countLeadingZeroBitsDigest(d), countLeadingZeroBits(hex.EncodeToString(d[:])); got != want {
			t.Fatalf("leading byte %d: got %d want %d", b, got, want)
		}
	}
}

func TestPoWDigestHelperMatchesHexHelper(t *testing.T) {
	for _, minDifficulty := range []int{0, 1, 4, 8, 12} {
		for claimed := 0; claimed <= 16; claimed++ {
			for leading := 0; leading <= 20; leading++ {
				var digest [32]byte
				// Set the first `leading` bits to zero and the next bit to one.
				for i := 0; i < leading; i++ {
					digest[i/8] &^= 1 << (7 - i%8)
				}
				if leading < 256 {
					digest[leading/8] |= 1 << (7 - leading%8)
				}
				ev := &nostr.Event{ID: hex.EncodeToString(digest[:]), Tags: nostr.Tags{{"nonce", "1", fmt.Sprint(claimed)}}}
				want := isPoWValid(ev, minDifficulty)
				if got := isPoWValidDigest(ev, minDifficulty, digest); got != want {
					t.Fatalf("min=%d claimed=%d leading=%d: got %t want %t", minDifficulty, claimed, leading, got, want)
				}
			}
		}
	}
	// Missing/short nonce tags and non-numeric difficulty claims agree too.
	ev := &nostr.Event{ID: strings.Repeat("0", 64), Tags: nostr.Tags{{"nonce", "1"}}}
	if isPoWValidDigest(ev, 1, [32]byte{}) != isPoWValid(ev, 1) {
		t.Fatal("short nonce tag disagreed")
	}
	ev.Tags = nostr.Tags{{"nonce", "1", "x"}}
	if isPoWValidDigest(ev, 1, [32]byte{}) != isPoWValid(ev, 1) {
		t.Fatal("invalid difficulty tag disagreed")
	}
	ev.Tags = nil
	if isPoWValidDigest(ev, 1, [32]byte{}) != isPoWValid(ev, 1) {
		t.Fatal("missing nonce tag disagreed")
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
		if countLeadingZeroBits(ev.ID) >= 1 {
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
