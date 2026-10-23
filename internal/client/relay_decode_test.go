package client

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

// directEventDecodeTestFrame wraps a signed event in the exact EVENT envelope
// the relay worker receives and returns the validated embedded object slice.
func directEventDecodeTestFrame(t testing.TB, event *nostr.Event) []byte {
	t.Helper()
	wire, err := json.Marshal([]any{"EVENT", "chat", event})
	if err != nil {
		t.Fatal(err)
	}
	var frame []json.RawMessage
	if err := json.Unmarshal(wire, &frame); err != nil || len(frame) != 3 {
		t.Fatalf("frame = %d elements, error %v", len(frame), err)
	}
	return frame[2]
}

// TestDirectEventDecodeMatchesStdlib proves that calling the generated event
// decoder directly, after the full outer envelope was validated by
// encoding/json, is equivalent to the previous json.Unmarshal call for valid,
// malformed and type-confused payloads. The stdlib route itself dispatches to
// Event.UnmarshalJSON; the only removed work is re-validating known-good JSON.
func TestDirectEventDecodeMatchesStdlib(t *testing.T) {
	long := strings.Repeat("ab ", 700) // 2100 bytes, well inside the 16-KiB content limit
	signed := func(content string, tags nostr.Tags) *nostr.Event {
		event := &nostr.Event{Kind: ephChatKind, CreatedAt: nostr.Now(), Tags: tags, Content: content}
		if err := event.Sign(transportTestKey); err != nil {
			t.Fatal(err)
		}
		return event
	}
	events := []*nostr.Event{
		signed("short", nostr.Tags{{"d", "lobby"}, {"n", "peer"}}),
		signed(long, nostr.Tags{{"d", "lobby"}}),
		signed("héllo wörld \x00\x01 \"quoted\"\n", nostr.Tags{{"d", "lobby"}, {"t", "unicode"}, {"t", "two"}}),
	}
	payloads := []string{
		`null`, `{}`, `[]`, `"text"`, `42`,
		`{"id":1}`, `{"created_at":"not-a-number"}`, `{"kind":1.5}`, `{"tags":{}}`,
		`{"id":null,"tags":null,"content":null}`,
		`{"unknown":"ignored","content":"kept"}`,
		`{"id":"` + strings.Repeat("a", 64) + `","kind":1,"created_at":123}`,
		// Not valid JSON: unreachable through the worker after the outer decode,
		// but the direct decoder must still reject rather than panic.
		`{`, `{"id":`, `nul`,
	}
	check := func(t *testing.T, payload []byte) {
		t.Helper()
		var viaStdlib, direct nostr.Event
		stdErr := json.Unmarshal(payload, &viaStdlib)
		directErr := direct.UnmarshalJSON(payload)
		if (stdErr == nil) != (directErr == nil) {
			t.Fatalf("payload %.80q: stdlib error %v, direct error %v", payload, stdErr, directErr)
		}
		if stdErr == nil && !reflect.DeepEqual(viaStdlib, direct) {
			t.Fatalf("payload %.80q: stdlib %+v, direct %+v", payload, viaStdlib, direct)
		}
	}
	for _, event := range events {
		payload := directEventDecodeTestFrame(t, event)
		check(t, payload)
		var decoded nostr.Event
		if err := decoded.UnmarshalJSON(payload); err != nil {
			t.Fatalf("valid event rejected: %v", err)
		}
		if ok, err := decoded.CheckSignature(); err != nil || !ok {
			t.Fatalf("direct decode changed signature validity: %v", err)
		}
		if decoded.ID != event.ID || decoded.PubKey != event.PubKey || decoded.Content != event.Content ||
			decoded.CreatedAt != event.CreatedAt || decoded.Kind != event.Kind || !reflect.DeepEqual(decoded.Tags, event.Tags) {
			t.Fatalf("direct decode changed event fields: %+v", decoded)
		}
	}
	for _, payload := range payloads {
		check(t, []byte(payload))
	}
}

func BenchmarkRelayEventDecode(b *testing.B) {
	short := directEventDecodeTestFrame(b, &nostr.Event{
		ID: strings.Repeat("a", 64), PubKey: strings.Repeat("b", 64), Sig: strings.Repeat("c", 128),
		Kind: ephChatKind, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", "lobby"}, {"n", "peer"}}, Content: "hello",
	})
	longEvent := &nostr.Event{
		ID: strings.Repeat("a", 64), PubKey: strings.Repeat("b", 64), Sig: strings.Repeat("c", 128),
		Kind: ephChatKind, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", "lobby"}}, Content: strings.Repeat("ab ", 700),
	}
	long := directEventDecodeTestFrame(b, longEvent)
	for _, tc := range []struct {
		name    string
		payload []byte
	}{{"short", short}, {"long", long}} {
		b.Run(tc.name+"/stdlib", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				var event nostr.Event
				if err := json.Unmarshal(tc.payload, &event); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(tc.name+"/direct", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				var event nostr.Event
				if err := event.UnmarshalJSON(tc.payload); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
