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
