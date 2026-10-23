package client

import (
	"fmt"
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

// These benchmarks capture owner-loop work that is not signature verification:
// mention resolution and the optional display-nick tag. They are intentionally
// independent of the crypto backend so before/after numbers stay comparable.

func mentionBenchmarkClient(b *testing.B, users int) (*client, string) {
	b.Helper()
	c, _ := testClient(b)
	for i := range users {
		pk := fmt.Sprintf("%064x", i+1)
		c.userContext.Add(pk, userContext{
			nick:        fmt.Sprintf("user-%d", i),
			chat:        "lobby",
			shortPubKey: pk[:4],
		})
	}
	last := fmt.Sprintf("%064x", users)
	return c, "@user-" + fmt.Sprint(users-1) + "#" + last + " hello"
}

func BenchmarkResolveMessageTargetMention(b *testing.B) {
	c, message := mentionBenchmarkClient(b, 256)
	b.ReportAllocs()
	for b.Loop() {
		_, _, target, err := c.resolveMessageTarget(message, "", "")
		if err != nil || target == "" {
			b.Fatalf("mention did not resolve: %v", err)
		}
	}
}

func BenchmarkResolveMessageTargetPlain(b *testing.B) {
	c, _ := mentionBenchmarkClient(b, 256)
	b.ReportAllocs()
	for b.Loop() {
		_, _, _, err := c.resolveMessageTarget("plain message without mention", "", "")
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkProcessMessageDisplayNick(b *testing.B) {
	for _, withTag := range []bool{false, true} {
		name := "fallback"
		if withTag {
			name = "tag"
		}
		b.Run(name, func(b *testing.B) {
			c, events := testClient(b)
			e := performanceEvent(b, "A short public chat message.")
			if withTag {
				e.Tags = append(e.Tags, nostr.Tag{"n", "alice"})
				if err := e.Sign(performanceTestKey); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			for b.Loop() {
				c.seenCache.Purge()
				c.processEvent(e, "local")
				<-events
			}
		})
	}
}
