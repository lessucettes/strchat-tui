package client

import (
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/rivo/uniseg"
)

// A balanced scalar avoids the atypical fixed-base cost of scalar-one fixtures.
const performanceTestKey = "b7e151628aed2a6abf7158809cf4f3c762e7160f38b4da56a784d9045190cfef"

func performanceLongContent(t testing.TB) string {
	t.Helper()
	body := strings.Repeat("👨‍👩‍👧‍👦", 360) + strings.Repeat("界", 500) + strings.Repeat("я", 80) + strings.Repeat("a", 1060)
	if len(body) != 11720 || uniseg.GraphemeClusterCount(body) != MaxMsgLen {
		t.Fatal("invalid long-message benchmark fixture")
	}
	return body
}

func performanceEvent(t testing.TB, content string) *nostr.Event {
	t.Helper()
	ev := &nostr.Event{Kind: ephChatKind, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", "lobby"}, {"n", "alice"}}, Content: content}
	if err := ev.Sign(performanceTestKey); err != nil {
		t.Fatal(err)
	}
	return ev
}
