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
	signer, err := newSessionSigner(signerTestKeys[1])
	if err != nil {
		t.Fatal(err)
	}
	body := "A short public chat message."
	if long {
		body = strings.Repeat("👨‍👩‍👧‍👦", 360) + strings.Repeat("界", 500) + strings.Repeat("я", 80) + strings.Repeat("a", 1060)
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
	signer, err := newSessionSigner(signerTestKeys[1])
	if err != nil {
		t.Fatal(err)
	}
	for _, difficulty := range []int{0, 1, 8, 12, 16} {
		ev := miningTestEvent(t, false)
		ev.Tags = ev.Tags[:2]
		result := executePublish(context.Background(), publishJob{event: ev, key: signerTestKeys[1], signer: signer, chat: "lobby", difficulty: difficulty})
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
	result := executePublish(ctx, publishJob{event: ev, key: signerTestKeys[1], signer: signer, difficulty: 16})
	if result.kind != "ERROR" || result.event.ID != "" || !strings.Contains(result.message, context.Canceled.Error()) {
		t.Fatalf("cancelled mining returned %+v", result)
	}
}
