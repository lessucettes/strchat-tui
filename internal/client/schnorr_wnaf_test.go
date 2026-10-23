package client

import (
	"encoding/hex"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/nbd-wtf/go-nostr"
	"gitlab.com/yawning/secp256k1-voi/secec/bitcoin"
)

func TestWarmVerifyMatchesColdAndRejectsForgedBinding(t *testing.T) {
	rng := &randReader{}
	keyA := "0000000000000000000000000000000000000000000000000000000000000001"
	keyB := "B7E151628AED2A6ABF7158809CF4F3C762E7160F38B4DA56A784D9045190CFEF"
	evA := randomSignedEvent(t, keyA, rng)
	evB := randomSignedEvent(t, keyB, rng)

	cache := newAuthorVerifyCache(authorVerifyCacheSize)
	authorA, digestCold, ok := verifyEventDigestWarm(evA, nil, cache)
	if !ok || cache.len() != 1 {
		t.Fatalf("cold verification did not retain exactly one table: ok=%t len=%d", ok, cache.len())
	}
	authorA2, digestWarm, ok := verifyEventDigestWarm(evA, authorA, cache)
	if !ok || digestWarm != digestCold || authorA2 != authorA {
		t.Fatal("warm verification disagreed with cold verification")
	}

	authorB, _, ok := verifyEventDigestWarm(evB, nil, cache)
	if !ok || cache.len() != 2 {
		t.Fatal("second author did not verify and retain a table")
	}

	// Simulate a forged owner label: B's cache slot pointing at a table built
	// for A. The key-bound check inside the bitcoin package must reject it.
	cache.tables.Add(evB.PubKey, bitcoin.NewAuthorVerifyTable(authorA.key))
	if _, _, ok := verifyEventDigestWarm(evB, authorB, cache); ok {
		t.Fatal("a table bound to another key authorized event B")
	}
}

func TestInvalidSignaturesNeverBuildTable(t *testing.T) {
	rng := &randReader{}
	ev := randomSignedEvent(t, "0000000000000000000000000000000000000000000000000000000000000001", rng)
	cache := newAuthorVerifyCache(authorVerifyCacheSize)

	for name, mutate := range map[string]func(*nostr.Event){
		"bad-signature": func(e *nostr.Event) { e.Sig = flipHex(e.Sig) },
		"bad-id":        func(e *nostr.Event) { e.ID = flipHex(e.ID) },
		"bad-content":   func(e *nostr.Event) { e.Content += "x" },
		"malformed-sig": func(e *nostr.Event) { e.Sig = e.Sig[:126] },
	} {
		bad := *ev
		mutate(&bad)
		if _, _, ok := verifyEventDigestWarm(&bad, nil, cache); ok {
			t.Fatalf("%s verified", name)
		}
		if cache.len() != 0 {
			t.Fatalf("%s constructed a table: len=%d", name, cache.len())
		}
	}
}

func TestAuthorVerifyCacheEviction(t *testing.T) {
	const authors = authorVerifyCacheSize + 1
	events := make([]*nostr.Event, authors)
	for i := range events {
		priv, err := btcec.NewPrivateKey()
		if err != nil {
			t.Fatal(err)
		}
		ev := &nostr.Event{Kind: ephChatKind, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", "lobby"}}, Content: "eviction fixture"}
		if err := ev.Sign(hex.EncodeToString(priv.Serialize())); err != nil {
			t.Fatal(err)
		}
		events[i] = ev
	}

	cache := newAuthorVerifyCache(authorVerifyCacheSize)
	for _, ev := range events {
		if _, _, ok := verifyEventDigestWarm(ev, nil, cache); !ok {
			t.Fatal("fixture event did not verify")
		}
	}
	if cache.len() != authorVerifyCacheSize {
		t.Fatalf("cache len = %d, want %d", cache.len(), authorVerifyCacheSize)
	}
	if cache.table(events[0].PubKey) != nil {
		t.Fatal("oldest author was not evicted")
	}
	if cache.table(events[authors-1].PubKey) == nil {
		t.Fatal("newest author missing")
	}
	// The evicted author can still verify cold and is retained again.
	if _, _, ok := verifyEventDigestWarm(events[0], nil, cache); !ok {
		t.Fatal("evicted author failed to verify cold")
	}
	if cache.table(events[0].PubKey) == nil || cache.table(events[1].PubKey) != nil {
		t.Fatal("re-insertion did not evict the next-oldest author")
	}
}
