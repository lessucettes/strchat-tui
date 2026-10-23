package client

import (
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

// BenchmarkVerifyEventLegacy is the pre-change inbound crypto pipeline:
// go-nostr CheckID followed by CheckSignature, each hashing the serialization.
func BenchmarkVerifyEventLegacy(b *testing.B) {
	ev := performanceEvent(b, "A short public chat message.")
	b.ReportAllocs()
	for b.Loop() {
		if !ev.CheckID() {
			b.Fatal("id")
		}
		if ok, _ := ev.CheckSignature(); !ok {
			b.Fatal("signature")
		}
	}
}

// BenchmarkVerifyEventDigestCold verifies with one canonical digest and parses
// the author key on every operation (no cache hit).
func BenchmarkVerifyEventDigestCold(b *testing.B) {
	ev := performanceEvent(b, "A short public chat message.")
	b.ReportAllocs()
	for b.Loop() {
		if _, _, ok := verifyEventDigest(ev, nil); !ok {
			b.Fatal("signature")
		}
	}
}

// BenchmarkVerifyEventDigestCached reuses the parsed author key, which is what
// the owner loop sees for a second event from the same author.
func BenchmarkVerifyEventDigestCached(b *testing.B) {
	ev := performanceEvent(b, "A short public chat message.")
	author, _, ok := verifyEventDigest(ev, nil)
	if !ok {
		b.Fatal("fixture")
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, _, ok := verifyEventDigest(ev, author); !ok {
			b.Fatal("signature")
		}
	}
}

func BenchmarkVerifyEventLongLegacy(b *testing.B) {
	ev := performanceEvent(b, performanceLongContent(b))
	b.ReportAllocs()
	for b.Loop() {
		if !ev.CheckID() {
			b.Fatal("id")
		}
		if ok, _ := ev.CheckSignature(); !ok {
			b.Fatal("signature")
		}
	}
}

func BenchmarkVerifyEventLongDigestCached(b *testing.B) {
	ev := performanceEvent(b, performanceLongContent(b))
	author, _, ok := verifyEventDigest(ev, nil)
	if !ok {
		b.Fatal("fixture")
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, _, ok := verifyEventDigest(ev, author); !ok {
			b.Fatal("signature")
		}
	}
}

func BenchmarkSessionSignerSign(b *testing.B) {
	key := performanceTestKey
	signer, err := newSessionSigner(key)
	if err != nil {
		b.Fatal(err)
	}
	ev := nostr.Event{Kind: ephChatKind, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", "lobby"}}, Content: "hello"}
	b.ReportAllocs()
	for b.Loop() {
		if err := signer.sign(&ev); err != nil {
			b.Fatal(err)
		}
	}
}
