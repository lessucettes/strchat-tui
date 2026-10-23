package client

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/coder/websocket"
	"github.com/nbd-wtf/go-nostr"
)

const (
	maxRelayChallengeBytes = 1024
	maxRelayChallenges     = 4
	relayChallengeWindow   = time.Minute
)

type relayAuthAttempt struct {
	id   string
	done bool
	err  error
}

// Authentication belongs to one socket and one challenge, never to a URL cache.
// NIP-42 requires relays to retain all successfully authenticated public keys.
// Only the worker accesses this state; private keys come from immutable snapshots.
type relayAuth struct {
	challenge  string
	challenged bool
	attempts   map[string]*relayAuthAttempt // public key -> result
	pending    map[string]string            // AUTH event ID -> public key
	changes    []time.Time                  // bounded sliding challenge-rate window
}

func (a *relayAuth) setChallenge(challenge string) (bool, error) {
	if len(challenge) > maxRelayChallengeBytes {
		return false, fmt.Errorf("relay authentication challenge exceeds %d bytes", maxRelayChallengeBytes)
	}
	if a.challenged && a.challenge == challenge {
		return false, nil
	}
	// Check before signing or resetting proof state. Even acknowledged challenge
	// churn must not turn one inbound frame into an unlimited group of signatures.
	now := time.Now()
	for len(a.changes) > 0 && now.Sub(a.changes[0]) >= relayChallengeWindow {
		a.changes = a.changes[1:]
	}
	if len(a.changes) >= maxRelayChallenges {
		return false, errors.New("relay authentication challenge changed too often")
	}
	a.changes = append(a.changes, now)
	a.challenge, a.challenged = challenge, true
	a.attempts = make(map[string]*relayAuthAttempt)
	a.pending = make(map[string]string)
	return true, nil
}

func (a *relayAuth) send(ctx context.Context, conn *websocket.Conn, url string, keys map[string]string) error {
	if !a.challenged {
		return nil
	}
	for pubkey, key := range keys {
		if a.attempts[pubkey] != nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		derived, err := nostr.GetPublicKey(key)
		if err != nil || derived != pubkey {
			return errors.New("no valid chat identity for relay authentication")
		}
		event := nostr.Event{
			Kind: nostr.KindClientAuthentication, CreatedAt: nostr.Now(),
			Tags: nostr.Tags{{"relay", url}, {"challenge", a.challenge}},
		}
		if err := event.Sign(key); err != nil {
			return errors.New("failed to sign relay authentication proof")
		}
		if err := writeFrame(ctx, conn, []any{"AUTH", event}); err != nil {
			return err
		}
		a.attempts[pubkey] = &relayAuthAttempt{id: event.ID}
		a.pending[event.ID] = pubkey
	}
	return nil
}

func (a *relayAuth) acknowledge(id string, accepted bool, reason string) (string, bool) {
	pubkey, exists := a.pending[id]
	if !exists {
		return "", false
	}
	delete(a.pending, id)
	attempt := a.attempts[pubkey]
	attempt.done = true
	if !accepted {
		attempt.err = relayRejection("relay authentication rejected", reason)
	}
	return pubkey, true
}

func (a *relayAuth) complete(keys map[string]string) bool {
	for pubkey := range keys {
		if attempt := a.attempts[pubkey]; attempt == nil || !attempt.done {
			return false
		}
	}
	return a.challenged
}

func (a *relayAuth) prune(keys map[string]string) {
	for pubkey, attempt := range a.attempts {
		if _, needed := keys[pubkey]; !needed {
			delete(a.pending, attempt.id)
			delete(a.attempts, pubkey)
		}
	}
}

func relayRejection(prefix, reason string) error {
	return fmt.Errorf("%s: %s", prefix, truncateString(sanitizeString(reason), 200))
}
