package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
)

// testCanonicalEventID is an independent NIP-01 oracle: stdlib JSON over the
// canonical array, including control characters the upstream fast serializer
// mishandles. These fixtures do not contain U+2028/U+2029, which stdlib JSON
// always escapes even though NIP-01 preserves them verbatim.
func testCanonicalEventID(t testing.TB, ev *nostr.Event) [32]byte {
	t.Helper()
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode([]any{0, ev.PubKey.Hex(), int64(ev.CreatedAt), int(ev.Kind), ev.Tags, ev.Content}); err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(bytes.TrimSuffix(data.Bytes(), []byte{'\n'}))
}

func miningTestEvent(t testing.TB, long bool) nostr.Event {
	t.Helper()
	signer, err := newSessionSigner(signerTestKeys[1])
	if err != nil {
		t.Fatal(err)
	}
	body := "A short <public> & chat message."
	if long {
		body = strings.Repeat("👨‍👩‍👧‍👦", 360) + strings.Repeat("界", 500) + strings.Repeat("я", 80) + strings.Repeat("a", 1060)
	}
	return nostr.Event{PubKey: signer.pubKey, Kind: ephChatKind, CreatedAt: 1700000000, Content: body,
		Tags: nostr.Tags{{"d", "lobby"}, {"n", "a\\\"\n界"}, {"nonce", "123", "16"}, {"x", "[\"nonce\",\"0\",\"16\"]"}, {"nonce", "0", "16"}}}
}

func TestMiningTemplateCanonicalEquivalence(t *testing.T) {
	control := miningTestEvent(t, false)
	control.Content = "control \b\x01\x1f text"
	control.Tags[3][1] = "\b\x02"
	for _, fixture := range []struct {
		name string
		ev   nostr.Event
	}{{"short", miningTestEvent(t, false)}, {"long", miningTestEvent(t, true)}, {"control", control}} {
		ev := fixture.ev
		miner, err := newMiningHasher(&ev)
		if err != nil {
			t.Fatal(err)
		}
		for _, nonce := range []uint64{0, 1, 9, 10, 99, 100, 999, 1000, 999999, 1000000, ^uint64(0), 0, 17} {
			ev.Tags[len(ev.Tags)-1][1] = strconv.FormatUint(nonce, 10)
			want := testCanonicalEventID(t, &ev)
			if got, err := miner.digest(nonce); err != nil || got != want {
				t.Fatalf("%s nonce=%d: %x != %x", fixture.name, nonce, got, want)
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
		if got := testCanonicalEventID(t, &result.event); got != result.event.ID {
			t.Fatalf("difficulty %d: no valid result: %s", difficulty, result.message)
		}
		if !result.event.VerifySignature() {
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
	if result.kind != "ERROR" || result.event.ID != nostr.ZeroID || !strings.Contains(result.message, context.Canceled.Error()) {
		t.Fatalf("cancelled mining returned %+v", result)
	}
}
