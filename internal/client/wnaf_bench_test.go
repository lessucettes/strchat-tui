package client

import (
	"encoding/hex"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/nbd-wtf/go-nostr"
)

func benchmarkAuthorEvents(b testing.TB, n int) []*nostr.Event {
	b.Helper()
	events := make([]*nostr.Event, n)
	for i := range events {
		priv, err := btcec.NewPrivateKey()
		if err != nil {
			b.Fatal(err)
		}
		ev := &nostr.Event{Kind: ephChatKind, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", "lobby"}, {"n", "bench"}}, Content: "A short public chat message."}
		if err := ev.Sign(hex.EncodeToString(priv.Serialize())); err != nil {
			b.Fatal(err)
		}
		events[i] = ev
	}
	return events
}

// BenchmarkVerifyEventWarmTable is a second and later event from one author:
// parsed key plus retained table, same checks as the cold path.
func BenchmarkVerifyEventWarmTable(b *testing.B) {
	ev := performanceEvent(b, "A short public chat message.")
	author, _, ok := verifyEventDigest(ev, nil)
	if !ok {
		b.Fatal("fixture")
	}
	cache := newAuthorVerifyCache(authorVerifyCacheSize)
	cache.retain(ev.PubKey, author.key)
	if cache.table(ev.PubKey) == nil {
		b.Fatal("table")
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, _, ok := verifyEventDigestWarm(ev, author, cache); !ok {
			b.Fatal("signature")
		}
	}
}

// BenchmarkVerifyEventManyAuthorsCold is the no-table path over 4096 distinct
// authors; every operation parses its author key and builds no retained table.
func BenchmarkVerifyEventManyAuthorsCold(b *testing.B) {
	events := benchmarkAuthorEvents(b, 4096)
	b.ReportAllocs()
	b.ResetTimer()
	i := 0
	for b.Loop() {
		if _, _, ok := verifyEventDigest(events[i&4095], nil); !ok {
			b.Fatal("signature")
		}
		i++
	}
}

// BenchmarkVerifyEventManyAuthorsWarm keeps 512 authors (the production cap)
// warm and rotates through them so every event takes the table path.
func BenchmarkVerifyEventManyAuthorsWarm(b *testing.B) {
	events := benchmarkAuthorEvents(b, authorVerifyCacheSize)
	authors := make([]*authorPublicKey, len(events))
	cache := newAuthorVerifyCache(authorVerifyCacheSize)
	for i, ev := range events {
		author, _, ok := verifyEventDigestWarm(ev, nil, cache)
		if !ok {
			b.Fatal("fixture")
		}
		authors[i] = author
	}
	if cache.len() != authorVerifyCacheSize {
		b.Fatalf("cache len = %d", cache.len())
	}
	b.ReportAllocs()
	b.ResetTimer()
	i := 0
	for b.Loop() {
		j := i & (authorVerifyCacheSize - 1)
		if _, _, ok := verifyEventDigestWarm(events[j], authors[j], cache); !ok {
			b.Fatal("signature")
		}
		i++
	}
}

func BenchmarkProcessEvent(b *testing.B) {
	ev := performanceEvent(b, "A short public chat message.")
	for _, warm := range []bool{false, true} {
		name := "cold"
		if warm {
			name = "warm"
		}
		b.Run(name, func(b *testing.B) {
			c, display := testClient(b)
			if !warm {
				c.authorVerify = nil
			}
			b.ReportAllocs()
			for b.Loop() {
				c.seenCache.Purge()
				c.processEvent(ev, "local")
				<-display
			}
		})
	}
}

func BenchmarkProcessEventManyAuthors(b *testing.B) {
	events := benchmarkAuthorEvents(b, authorVerifyCacheSize)
	for _, warm := range []bool{false, true} {
		name := "cold"
		if warm {
			name = "warm"
		}
		b.Run(name, func(b *testing.B) {
			c, display := testClient(b)
			if !warm {
				c.authorVerify = nil
			}
			b.ReportAllocs()
			i := 0
			for b.Loop() {
				c.seenCache.Purge()
				c.processEvent(events[i&(authorVerifyCacheSize-1)], "local")
				<-display
				i++
			}
		})
	}
}
