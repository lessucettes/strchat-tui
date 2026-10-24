package client

import (
	"context"
	"crypto/sha256"
	"strconv"
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

func miningTestEvent(t testing.TB, long bool) nostr.Event {
	t.Helper()
	signer, err := newSessionSigner(performanceTestKey)
	if err != nil {
		t.Fatal(err)
	}
	body := "A short public chat message."
	if long {
		body = performanceLongContent(t)
	}
	return nostr.Event{PubKey: signer.pubKey, Kind: ephChatKind, CreatedAt: 1700000000, Content: body,
		Tags: nostr.Tags{{"d", "lobby"}, {"n", "a\\\"\n界"}, {"nonce", "123", "16"}, {"x", "[\"nonce\",\"0\",\"16\"]"}, {"nonce", "0", "16"}}}
}

func TestMiningTemplateCanonicalEquivalence(t *testing.T) {
	for _, long := range []bool{false, true} {
		ev := miningTestEvent(t, long)
		miner, err := newMiningHasher(&ev)
		if err != nil {
			t.Fatal(err)
		}
		for _, nonce := range []uint64{0, 1, 9, 10, 99, 100, 999, 1000, 999999, 1000000, ^uint64(0), 0, 17} {
			ev.Tags[len(ev.Tags)-1][1] = strconv.FormatUint(nonce, 10)
			want := sha256.Sum256(ev.Serialize())
			if got, err := miner.digest(nonce); err != nil || got != want {
				t.Fatalf("long=%v nonce=%d: %x != %x", long, nonce, got, want)
			}
		}
	}
}

func TestExecutePublishMinedProofMatchesSignedEvent(t *testing.T) {
	signer, err := newSessionSigner(performanceTestKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, difficulty := range []int{0, 1, 8, 12, 16} {
		ev := miningTestEvent(t, false)
		ev.Tags = ev.Tags[:2]
		result := executePublish(context.Background(), publishJob{event: ev, key: performanceTestKey, signer: signer, chat: "lobby", difficulty: difficulty})
		if !result.event.CheckID() {
			t.Fatalf("difficulty %d: no valid result: %s", difficulty, result.message)
		}
		if ok, _ := result.event.CheckSignature(); !ok {
			t.Fatalf("difficulty %d: signature", difficulty)
		}
		if !isPoWValid(&result.event, difficulty) {
			t.Fatalf("difficulty %d: invalid final proof", difficulty)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ev := miningTestEvent(t, false)
	result := executePublish(ctx, publishJob{event: ev, key: performanceTestKey, signer: signer, difficulty: 16})
	if result.kind != "ERROR" || result.event.ID != "" || !strings.Contains(result.message, context.Canceled.Error()) {
		t.Fatalf("cancelled mining returned %+v", result)
	}
}

// Compare full-buffer hashing with the roadmap's prefix hash-state restore.
// Both reuse authoritative serialization and the same structurally found slot.
func BenchmarkNonceHash(b *testing.B) {
	for _, long := range []bool{false, true} {
		name := "short"
		if long {
			name = "long"
		}
		b.Run(name, func(b *testing.B) {
			ev := miningTestEvent(b, long)
			ev.Tags = append(ev.Tags[:2:2], nostr.Tag{"nonce", "0", "16"})
			b.Run("legacy", func(b *testing.B) {
				var nonce uint64
				b.ReportAllocs()
				for b.Loop() {
					ev.Tags[len(ev.Tags)-1][1] = strconv.FormatUint(nonce, 10)
					miningStringResult = ev.GetID()
					nonce++
				}
			})
			ev.Tags[len(ev.Tags)-1][1] = "0"
			b.Run("template", func(b *testing.B) {
				miner, err := newNonceTemplate(&ev)
				if err != nil {
					b.Fatal(err)
				}
				var nonce uint64
				b.ReportAllocs()
				for b.Loop() {
					miningDigestResult = templateNonceDigest(miner, nonce)
					nonce++
				}
			})
			b.Run("prefix-state", func(b *testing.B) {
				miner, err := newMiningHasher(&ev)
				if err != nil {
					b.Fatal(err)
				}
				var nonce uint64
				b.ReportAllocs()
				for b.Loop() {
					miningDigestResult, err = miner.digest(nonce)
					if err != nil {
						b.Fatal(err)
					}
					nonce++
				}
			})
		})
	}
}

var miningStringResult string
var miningDigestResult [32]byte

// Rejected full-template alternative retained only as a benchmark comparator.
func templateNonceDigest(h *nonceTemplate, nonce uint64) [32]byte {
	var buf [20]byte
	digits := strconv.AppendUint(buf[:0], nonce, 10)
	if growth := len(digits) - (h.end - h.start); growth != 0 {
		oldLen, oldEnd := len(h.canonical), h.end
		if growth > 0 {
			h.canonical = append(h.canonical, make([]byte, growth)...)
		}
		copy(h.canonical[oldEnd+growth:], h.canonical[oldEnd:oldLen])
		h.canonical = h.canonical[:oldLen+growth]
		h.end += growth
	}
	copy(h.canonical[h.start:h.end], digits)
	return sha256.Sum256(h.canonical)
}
