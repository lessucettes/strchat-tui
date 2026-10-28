package client

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
)

func testClient(t testing.TB) (*client, chan DisplayEvent) {
	t.Helper()
	events := make(chan DisplayEvent, 1024)
	c := newClient(&config{Art: DefaultArt, Views: []View{{Name: "lobby"}}, ActiveViewName: "lobby"}, nil, events)
	t.Cleanup(c.cancel)
	return c, events
}

func signedEvent(t testing.TB, content string) *nostr.Event {
	t.Helper()
	e := &nostr.Event{Kind: ephChatKind, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", "lobby"}, {"n", "alice"}}, Content: content}
	if err := e.Sign(nostr.MustSecretKeyFromHex(strings.Repeat("0", 63) + "1")); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestProcessEventRejectsUnrelatedTraffic(t *testing.T) {
	c, events := testClient(t)
	e := signedEvent(t, "not in the active chat")
	e.Tags[0][1] = "other"
	if err := e.Sign(nostr.MustSecretKeyFromHex(strings.Repeat("0", 63) + "1")); err != nil {
		t.Fatal(err)
	}
	c.processEvent(e, "local")

	select {
	case e := <-events:
		t.Fatalf("unrelated event displayed: %s", e.Type)
	default:
	}
}

func TestProcessEventValidationAndDedup(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*nostr.Event)
	}{
		{"bad-id", func(e *nostr.Event) { e.ID = nostr.MustIDFromHex(strings.Repeat("f", 64)) }},
		{"bad-signature", func(e *nostr.Event) { e.Sig = [64]byte{} }},
		{"wrong-kind", func(e *nostr.Event) { e.Kind = 1 }},
		{"wrong-tag", func(e *nostr.Event) { e.Tags = nostr.Tags{{"g", "lobby"}} }},
		{"future", func(e *nostr.Event) { e.CreatedAt = nostr.Now() + 3600 }},
		{"old", func(e *nostr.Event) { e.CreatedAt = nostr.Now() - 3600 }},
		{"expired", func(e *nostr.Event) { e.Tags = append(e.Tags, nostr.Tag{"expiration", "1"}) }},
		{"oversized", func(e *nostr.Event) { e.Content = strings.Repeat("x", maxContentBytes+1) }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			c, events := testClient(t)
			original := signedEvent(t, "hello")
			bad := *original
			bad.Tags = append(nostr.Tags(nil), original.Tags...)
			tt.mutate(&bad)
			switch tt.name {
			case "bad-id", "bad-signature":
			default:
				// Keep policy probes signed so an invalid ID cannot mask a missing guard.
				if err := bad.Sign(nostr.MustSecretKeyFromHex(strings.Repeat("0", 63) + "1")); err != nil {
					t.Fatal(err)
				}
			}
			c.processEvent(&bad, "local")
			if len(events) != 0 {
				t.Fatal("invalid event displayed")
			}
			c.processEvent(original, "local")
			c.processEvent(original, "second relay")
			if len(events) != 1 {
				t.Fatalf("valid event poisoned or duplicate displayed: %d", len(events))
			}
		})
	}
}

func TestPublishKeepsAcknowledgementsReceivedBeforeCancellation(t *testing.T) {
	for range 32 {
		ctx, cancel := context.WithCancel(context.Background())
		slow := &managedRelay{publish: make(chan relayPublish, 1)}
		healthy := &managedRelay{publish: make(chan relayPublish, 1)}
		done := make(chan struct{})
		go func() {
			defer close(done)
			request := <-healthy.publish
			request.result <- nil
			cancel() // The first relay never answers; the second already accepted.
		}()
		result := executePublish(ctx, publishJob{event: *signedEvent(t, "accepted before deadline"), key: strings.Repeat("0", 63) + "1", relays: []*managedRelay{slow, healthy}})
		<-done
		cancel()
		if result.accepted != 1 || result.kind != "WARN" {
			t.Fatalf("partial acknowledgement must be preserved and reported as WARN: accepted=%d kind=%s", result.accepted, result.kind)
		}
		if strings.Contains(result.message, context.Canceled.Error()) || !strings.Contains(result.message, "No automatic resend") {
			t.Fatalf("partial acknowledgement needs a concise warning without a transport error: %s", result.message)
		}
	}
}

func TestPublishWithoutAcknowledgementExplainsFailure(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		name := "busy"
		if stopped {
			name = "stopped"
		}
		t.Run(name, func(t *testing.T) {
			worker := &managedRelay{publish: make(chan relayPublish, 1), done: make(chan struct{})}
			worker.publish <- relayPublish{} // No capacity for this send.
			if stopped {
				close(worker.done)
			}
			result := executePublish(context.Background(), publishJob{
				event: *signedEvent(t, "no acknowledgement"), key: strings.Repeat("0", 63) + "1",
				chat: "lobby", relays: []*managedRelay{worker},
			})
			if result.kind != "ERROR" || result.accepted != 0 || !strings.Contains(result.message, "acknowledged by 0/1") || !strings.Contains(result.message, "unavailable or busy") || !strings.Contains(result.message, "No automatic resend") || strings.Contains(result.message, "other relays") {
				t.Fatalf("missing clear failure reason when %s: %+v", name, result)
			}
		})
	}
}

func FuzzProcessEvent(f *testing.F) {
	event := signedEvent(f, "hello")
	raw, _ := json.Marshal(event)
	f.Add(raw)
	f.Add([]byte(`{"kind":23333,"tags":[[]],"pubkey":"a"}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > maxFrameBytes {
			t.Skip()
		}
		var e nostr.Event
		if json.Unmarshal(data, &e) != nil {
			return
		}
		c, events := testClient(t)
		c.processEvent(&e, "fuzz")
		if len(events) > 1 {
			t.Fatal("one event produced multiple messages")
		}
	})
}
