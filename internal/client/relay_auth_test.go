package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/nbd-wtf/go-nostr"
)

// checkAuthProof checks the actual wire proof, not just the AUTH label.
func checkAuthProof(ts *relayTestServer, frame []json.RawMessage, challenge string) (nostr.Event, bool) {
	var label string
	var event nostr.Event
	if len(frame) != 2 || json.Unmarshal(frame[0], &label) != nil || label != "AUTH" || json.Unmarshal(frame[1], &event) != nil {
		ts.t.Errorf("expected an AUTH proof, got %s", mustMarshal(frame))
		return event, false
	}
	valid, err := event.CheckSignature()
	relay, nonce := event.Tags.Find("relay"), event.Tags.Find("challenge")
	if err != nil || !valid || event.ID != event.GetID() || event.Kind != nostr.KindClientAuthentication || event.Content != "" ||
		len(event.Tags) != 2 || len(relay) != 2 || relay[1] != ts.url ||
		len(nonce) != 2 || nonce[1] != challenge || event.CreatedAt < nostr.Now()-10 || event.CreatedAt > nostr.Now()+10 {
		ts.t.Errorf("invalid AUTH proof: kind=%d signature=%v error=%v", event.Kind, valid, err)
		return event, false
	}
	return event, true
}

func TestRelayAuthResubscribesGroupAndUsesFreshReconnectChallenge(t *testing.T) {
	proofs := make(chan nostr.Event, 4)
	ts := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, index int) {
		if !relayTestWaitREQ(ts, conn) {
			return
		}
		challenge := fmt.Sprintf("challenge-%d\x00\"exact", index)
		if !relayWriteJSON(ts, conn, []any{"AUTH", challenge}) ||
			!relayWriteJSON(ts, conn, relayClosedFrame("chat", "auth-required: authenticate both chat identities")) {
			return
		}
		keys := make(map[string]bool)
		for len(keys) < 2 {
			frame, ok := relayNextFrame(ts, conn)
			if !ok {
				return
			}
			proof, ok := checkAuthProof(ts, frame, challenge)
			if !ok {
				return
			}
			if keys[proof.PubKey] {
				ts.t.Error("AUTH repeated the same key instead of authenticating the other chat")
				return
			}
			keys[proof.PubKey] = true
			proofs <- proof
			if !relayWriteJSON(ts, conn, relayOKFrame(proof.ID, true, "")) {
				return
			}
		}
		if !relayTestWaitREQ(ts, conn) {
			return
		}
		if event := freshChatEvent(ts, transportTestKey, "coffee", index); event != nil {
			relayWriteJSON(ts, conn, relayEventFrame("chat", event))
		}
		if index == 1 {
			conn.CloseNow()
			return
		}
		relayServeAckFrames(ts, conn, true, "")
	})
	cfg := relayTestConfig(t, ts.url)
	cfg.Views = []View{{Name: "mixed", IsGroup: true, Children: []string{"coffee", "lobby"}, SelectedChat: "coffee"}, {Name: "coffee"}, {Name: "lobby"}}
	cfg.ActiveViewName = "mixed"
	h := startRelayHarness(t, cfg, nil)
	h.waitMessage("coffee-2")
	var firstKeys = make(map[string]bool)
	for i := range 4 {
		select {
		case proof := <-proofs:
			if i < 2 {
				firstKeys[proof.PubKey] = true
			} else if !firstKeys[proof.PubKey] {
				t.Fatal("reconnecting changed the chat signing identity")
			}
		case <-time.After(transportWaitOp):
			t.Fatal("missing AUTH proof")
		}
	}
	h.send(UserAction{Type: "SEND_MESSAGE", View: "mixed", Chat: "coffee", Payload: "after auth"})
	h.waitForEvent("authenticated publish", func(event DisplayEvent) bool {
		return event.Type == "STATUS" && strings.Contains(event.Content, "acknowledged by 1/1")
	})
	select {
	case event := <-ts.eventCh:
		if !firstKeys[event.PubKey] || event.Kind == nostr.KindClientAuthentication {
			t.Fatal("publish used an identity other than the authenticated chat keys")
		}
	case <-time.After(transportWaitOp):
		t.Fatal("authenticated EVENT never reached the relay")
	}
}

func TestRelayAuthPublishRetryPreservesEventAndAcknowledgement(t *testing.T) {
	for _, test := range []struct {
		name       string
		authReject bool
		noEventAck bool
		reason     string
		wantError  string
	}{
		{name: "accepted"},
		{name: "AUTH OK is not EVENT OK", noEventAck: true, wantError: "deadline exceeded"},
		{name: "restricted publication", reason: "restricted: not a member", wantError: "restricted:"},
		{name: "AUTH denied", authReject: true, wantError: "authentication rejected: restricted:"},
		{name: "repeated auth-required", reason: "auth-required: still refusing", wantError: "auth-required:"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ts := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, _ int) {
				if !relayTestWaitREQ(ts, conn) {
					return
				}
				frame, ok := relayNextLabeledFrame(ts, conn, "EVENT")
				if !ok {
					return
				}
				var original nostr.Event
				if len(frame) != 2 || json.Unmarshal(frame[1], &original) != nil {
					t.Error("invalid initial EVENT")
					return
				}
				if valid, err := original.CheckSignature(); err != nil || !valid {
					t.Error("initial EVENT has no valid signature")
					return
				}
				ts.noteEvent(original)
				// Also accept relays that deliver their challenge after the rejection.
				if !relayWriteJSON(ts, conn, relayOKFrame(original.ID, false, "auth-required: prove ownership")) ||
					!relayWriteJSON(ts, conn, []any{"AUTH", "write-challenge"}) {
					return
				}
				keys := make(map[string]bool)
				for len(keys) < 2 {
					frame, ok = relayNextFrame(ts, conn)
					if !ok {
						return
					}
					proof, valid := checkAuthProof(ts, frame, "write-challenge")
					if !valid || keys[proof.PubKey] {
						t.Error("missing or duplicate AUTH identity for a retained publish job")
						return
					}
					keys[proof.PubKey] = true
					accepted, reason := true, ""
					if test.authReject && proof.PubKey == original.PubKey {
						accepted, reason = false, "restricted: not a member"
					}
					if !relayWriteJSON(ts, conn, relayOKFrame(proof.ID, accepted, reason)) {
						return
					}
				}
				if !keys[original.PubKey] {
					t.Error("AUTH used a replacement key instead of the queued event's key")
					return
				}
				if test.authReject {
					relayServeAckFrames(ts, conn, false, "restricted: denied")
					return
				}
				frame, ok = relayNextFrame(ts, conn)
				if !ok {
					return
				}
				var label string
				var retried nostr.Event
				if len(frame) != 2 || json.Unmarshal(frame[0], &label) != nil || label != "EVENT" || json.Unmarshal(frame[1], &retried) != nil ||
					string(mustMarshal(retried)) != string(mustMarshal(original)) {
					t.Error("authentication retry changed or replaced the original signed EVENT")
					return
				}
				ts.noteEvent(retried)
				if !test.noEventAck {
					relayWriteJSON(ts, conn, relayOKFrame(retried.ID, test.reason == "", test.reason))
				}
				relayServeAckFrames(ts, conn, false, "auth-required: no further retries allowed")
			})
			c := transportClient(t, relayTestConfig(t, ts.url))
			drainRelayReports(t, c)
			c.updateRelaySubscriptions(map[string][]string{ts.url: {"lobby"}})
			ts.waitREQs(1, transportWaitSlow)
			ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
			defer cancel()
			// This accepted job's identity is no longer in the current subscription.
			result := executePublish(ctx, publishJob{key: transportTestKey, chat: "lobby",
				event:  nostr.Event{Kind: ephChatKind, Content: "immutable auth retry", Tags: nostr.Tags{{"d", "lobby"}}},
				relays: []*managedRelay{c.relays[ts.url]},
			})
			if test.wantError == "" {
				if result.accepted != 1 || result.kind != "STATUS" {
					t.Fatalf("authenticated publish failed: %+v", result)
				}
			} else if result.accepted != 0 || result.kind != "ERROR" || !strings.Contains(result.message, test.wantError) {
				t.Fatalf("wrong failure or false publish success: %+v", result)
			}
			want := int32(2)
			if test.authReject {
				want = 1
			}
			if got := ts.events.Load(); got != want {
				t.Fatalf("relay received %d events, want %d; authentication retried indefinitely or lost a send", got, want)
			}
		})
	}
}

func TestRelayAuthChallengeReplacementHintAndViewChange(t *testing.T) {
	ts := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, _ int) {
		if !relayTestWaitREQ(ts, conn) || !relayWriteJSON(ts, conn, []any{"AUTH", "obsolete"}) {
			return
		}
		frame, ok := relayNextFrame(ts, conn)
		if !ok {
			return
		}
		old, ok := checkAuthProof(ts, frame, "obsolete")
		if !ok {
			return
		}
		// An empty challenge is still a literal challenge. Late old OKs must not
		// complete the replacement exchange; unknown EOSE hints are harmless.
		if !relayWriteJSON(ts, conn, []any{"AUTH", ""}) ||
			!relayWriteJSON(ts, conn, relayOKFrame(old.ID, true, "")) ||
			!relayWriteJSON(ts, conn, []any{"EOSE", "chat", []string{"future-hint", "auth"}}) {
			return
		}
		frame, ok = relayNextFrame(ts, conn)
		if !ok {
			return
		}
		current, ok := checkAuthProof(ts, frame, "")
		if !ok || current.ID == old.ID || current.PubKey != old.PubKey {
			ts.t.Error("replacement AUTH did not use the current challenge and original identity")
			return
		}
		if !relayWriteJSON(ts, conn, relayOKFrame(current.ID, true, "")) ||
			!relayWriteJSON(ts, conn, relayOKFrame(old.ID, false, "restricted: stale proof")) ||
			!relayTestWaitREQ(ts, conn) {
			return
		}
		if event := freshChatEvent(ts, transportTestKey, "lobby", 1); event != nil {
			relayWriteJSON(ts, conn, relayEventFrame("chat", event))
		}
		frame, ok = relayNextFrame(ts, conn)
		if !ok {
			return
		}
		next, ok := checkAuthProof(ts, frame, "")
		if !ok || next.PubKey == current.PubKey {
			ts.t.Error("new chat did not authenticate its own identity on the existing socket")
			return
		}
		if !relayWriteJSON(ts, conn, relayOKFrame(next.ID, true, "")) || !relayTestWaitREQ(ts, conn) {
			return
		}
		if event := freshChatEvent(ts, transportTestKey, "other", 2); event != nil {
			relayWriteJSON(ts, conn, relayEventFrame("chat", event))
		}
		relayReadUntilClosed(ts, conn)
	})
	h := startRelayHarness(t, relayTestConfig(t, ts.url), nil)
	h.waitMessage("lobby-1")
	h.send(UserAction{Type: "JOIN_CHATS", Payload: "other"})
	h.waitMessage("other-2")
	if ts.conns.Load() != 1 || ts.reqs.Load() != 3 {
		t.Fatalf("authentication or view changes churned the socket/subscription: connections=%d requests=%d", ts.conns.Load(), ts.reqs.Load())
	}
	for _, event := range h.snapshot() {
		if event.Type == "ERROR" {
			t.Fatalf("stale authentication response affected the new exchange: %q", event.Content)
		}
	}
}

func TestRelayAuthSubscriptionFailuresAreBoundedAndVisible(t *testing.T) {
	for _, mode := range []string{"restricted", "AUTH denied", "AUTH unanswered", "repeated auth-required"} {
		t.Run(mode, func(t *testing.T) {
			ts := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, _ int) {
				if !relayTestWaitREQ(ts, conn) {
					return
				}
				if mode == "restricted" {
					relayWriteJSON(ts, conn, relayClosedFrame("chat", "restricted: membership required"))
					relayReadUntilClosed(ts, conn)
					return
				}
				if !relayWriteJSON(ts, conn, []any{"AUTH", "denied-challenge"}) ||
					!relayWriteJSON(ts, conn, relayClosedFrame("chat", "auth-required: proof required")) {
					return
				}
				frame, ok := relayNextFrame(ts, conn)
				if !ok {
					return
				}
				proof, ok := checkAuthProof(ts, frame, "denied-challenge")
				if !ok {
					return
				}
				if mode == "AUTH denied" {
					relayWriteJSON(ts, conn, relayOKFrame(proof.ID, false, "restricted: membership required"))
				} else if mode == "repeated auth-required" {
					if !relayWriteJSON(ts, conn, relayOKFrame(proof.ID, true, "")) || !relayTestWaitREQ(ts, conn) {
						return
					}
					relayWriteJSON(ts, conn, relayClosedFrame("chat", "auth-required: still refusing"))
				}
				relayReadUntilClosed(ts, conn)
			})
			h := startRelayHarness(t, relayTestConfig(t, ts.url), nil)
			h.waitForEvent("visible authentication/access failure", func(event DisplayEvent) bool {
				return event.Type == "WARN" && (strings.Contains(event.Content, "restricted:") || strings.Contains(event.Content, "timed out") || strings.Contains(event.Content, "still requires authentication"))
			})
			ts.waitClosed(1, transportWaitSlow)
			// The old reconnect loop retries in 250-500ms. A policy/auth failure
			// must instead enter the longer existing URL-level cooldown.
			time.Sleep(650 * time.Millisecond)
			if got := ts.conns.Load(); got != 1 {
				t.Fatalf("permanent authentication/access failure caused reconnect churn: %d sockets", got)
			}
			h.stopWithin(time.Second)
		})
	}
}

func TestRelayAuthChallengeBudgetExpires(t *testing.T) {
	var auth relayAuth
	for i := range 4 {
		if changed, err := auth.setChallenge(fmt.Sprintf("initial-%d", i)); err != nil || !changed {
			t.Fatalf("legitimate challenge refused: %v", err)
		}
	}
	// Advance the recorded sliding window rather than sleeping for a minute.
	for i := range auth.changes {
		auth.changes[i] = time.Now().Add(-2 * time.Minute)
	}
	for i := range 4 {
		if changed, err := auth.setChallenge(fmt.Sprintf("later-%d", i)); err != nil || !changed {
			t.Fatalf("expired challenges still consumed the fresh budget: %v", err)
		}
	}
	if changed, err := auth.setChallenge("over-budget"); err == nil || changed {
		t.Fatal("expiring the old window disabled enforcement for new challenges")
	}
}

func TestRelayAuthRejectsChallengeAmplification(t *testing.T) {
	for _, mode := range []string{"oversized", "churn"} {
		t.Run(mode, func(t *testing.T) {
			allowed, wantError := 0, "challenge exceeds"
			if mode == "churn" {
				allowed, wantError = 4, "challenge changed too often"
			}
			proofs := make(chan nostr.Event, 5)
			ts := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, _ int) {
				if !relayTestWaitREQ(ts, conn) {
					return
				}
				for i := range allowed {
					challenge := fmt.Sprintf("bounded-challenge-%d", i)
					if !relayWriteJSON(ts, conn, []any{"AUTH", challenge}) {
						return
					}
					frame, ok := relayNextFrame(ts, conn)
					if !ok {
						return
					}
					proof, ok := checkAuthProof(ts, frame, challenge)
					if !ok {
						return
					}
					select {
					case proofs <- proof:
					default:
						ts.t.Error("challenge flood exceeded the proof recording budget")
						return
					}
					if !relayWriteJSON(ts, conn, relayOKFrame(proof.ID, true, "")) {
						return
					}
					// Duplicate challenges must neither create proofs nor consume the
					// budget for legitimate replacement challenges.
					for range 3 {
						if !relayWriteJSON(ts, conn, []any{"AUTH", challenge}) {
							return
						}
					}
				}
				challenge := "one-change-too-many"
				if mode == "oversized" {
					challenge = strings.Repeat("x", 1025)
				}
				if !relayWriteJSON(ts, conn, []any{"AUTH", challenge}) {
					return
				}
				if frame, ok := relayNextFrame(ts, conn); ok {
					ts.t.Errorf("relay amplified its challenge into another client frame: %d bytes", len(mustMarshal(frame)))
				}
			})
			h := startRelayHarness(t, relayTestConfig(t, ts.url), nil)
			h.waitForEvent("challenge abuse refused before signing", func(event DisplayEvent) bool {
				return event.Type == "WARN" && strings.Contains(event.Content, wantError)
			})
			ts.waitClosed(1, transportWaitSlow)
			if got := len(proofs); got != allowed {
				t.Fatalf("signed %d proofs, want bounded budget of %d", got, allowed)
			}
			h.stopWithin(time.Second)
		})
	}
}
