package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

func testClient(t testing.TB) (*client, chan DisplayEvent) {
	t.Helper()
	events := make(chan DisplayEvent, 1024)
	c := newClient(&config{Views: []View{{Name: "lobby"}}, ActiveViewName: "lobby"}, nil, events)
	t.Cleanup(c.cancel)
	return c, events
}

func signedEvent(t testing.TB, content string) *nostr.Event {
	t.Helper()
	e := &nostr.Event{Kind: ephChatKind, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", "lobby"}, {"n", "alice"}}, Content: content}
	if err := e.Sign(strings.Repeat("0", 63) + "1"); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestProcessEventRejectsUnrelatedTraffic(t *testing.T) {
	c, events := testClient(t)
	e := signedEvent(t, "not in the active chat")
	e.Tags[0][1] = "other"
	if err := e.Sign(strings.Repeat("0", 63) + "1"); err != nil {
		t.Fatal(err)
	}
	c.processEvent(e, "local")

	select {
	case e := <-events:
		t.Fatalf("unrelated event displayed: %s", e.Type)
	default:
	}
}

func BenchmarkProcessDuplicate(b *testing.B) {
	c, _ := testClient(b)
	e := signedEvent(b, "hello")
	c.processEvent(e, "local")
	b.ResetTimer()
	for b.Loop() {
		c.processEvent(e, "local")
	}
}

func BenchmarkEventValidation(b *testing.B) {
	e := signedEvent(b, "hello")
	b.ReportAllocs()
	for b.Loop() {
		if !e.CheckID() {
			b.Fatal("id")
		}
		if ok, _ := e.CheckSignature(); !ok {
			b.Fatal("sig")
		}
	}
}

func BenchmarkEventSigning(b *testing.B) {
	key := strings.Repeat("0", 63) + "1"
	for b.Loop() {
		e := nostr.Event{Kind: ephChatKind, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", "lobby"}}, Content: fmt.Sprint("hello")}
		if err := e.Sign(key); err != nil {
			b.Fatal(err)
		}
	}
}

func TestProcessEventValidationAndDedup(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*nostr.Event)
	}{
		{"bad-id", func(e *nostr.Event) { e.ID = strings.Repeat("f", 64) }},
		{"bad-signature", func(e *nostr.Event) { e.Sig = strings.Repeat("0", 128) }},
		{"short-pubkey", func(e *nostr.Event) { e.PubKey = "1" }},
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

func TestPublishSnapshotsIdentityAndChat(t *testing.T) {
	c, _ := testClient(t)
	key := strings.Repeat("0", 63) + "1"
	pk, _ := nostr.GetPublicKey(key)
	c.chatKeys["lobby"] = chatSession{privKey: key, pubKey: pk, nick: "alice"}
	c.relays["local"] = &managedRelay{url: "local", connected: true, chats: []string{"lobby"}}
	c.publishMessage("hello")
	job := <-c.outgoing
	c.config.ActiveViewName = "other"
	c.chatKeys["lobby"] = chatSession{}
	if job.chat != "lobby" || job.key != key || job.event.PubKey != pk || job.event.Tags.Find("n")[1] != "alice" {
		t.Fatal("queued send did not capture identity and chat")
	}
}

func TestMiningCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := executePublish(ctx, publishJob{difficulty: 32, event: *signedEvent(t, "cancel"), key: strings.Repeat("0", 63) + "1"})
	if result.kind != "ERROR" || result.accepted != 0 {
		t.Fatal("cancelled mining reported success")
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

func BenchmarkProcessMessage(b *testing.B) {
	c, events := testClient(b)
	e := performanceEvent(b, "A short public chat message.")
	b.ReportAllocs()
	for b.Loop() {
		c.seenCache.Purge()
		c.processEvent(e, "local")
		<-events
	}
}

func BenchmarkClientInitialization(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		c := newClient(&config{}, nil, nil)
		c.Stop()
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
