package client

import (
	"context"

	"errors"
	"fmt"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
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
	// signers caches the immutable identity validated for this socket. Keys do
	// not rotate for a subscription; reusing the parsed signer avoids deriving
	// the same public key again for every challenge.
	signers map[string]*sessionSigner
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

func (a *relayAuth) send(ctx context.Context, conn relayConn, url string, keys map[string]string) error {
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
		signer, err := a.signerFor(pubkey, key)
		if err != nil {
			return err
		}
		event := nostr.Event{
			Kind: nostr.KindClientAuthentication, CreatedAt: nostr.Now(),
			Tags: nostr.Tags{{"relay", url}, {"challenge", a.challenge}},
		}
		if err := signer.sign(&event); err != nil {
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

// signerFor returns the immutable signer for an AUTH snapshot, deriving and
// validating it once per socket. A signer cached under pubkey necessarily has a
// matching derived public key, so a later snapshot cannot substitute another
// identity for the same key.
func (a *relayAuth) signerFor(pubkey, key string) (*sessionSigner, error) {
	if signer := a.signers[pubkey]; signer != nil {
		// Fail closed if a later snapshot pairs the same public key label with
		// a different secret, matching the old derive-and-compare behavior.
		var raw [32]byte
		if !decodeFixedHex(raw[:], key) {
			return nil, errors.New("no valid chat identity for relay authentication")
		}
		// Match newSessionSigner's modulo-N normalization, including its
		// noncanonical direct-call inputs, without re-deriving a public point.
		var scalar btcec.ModNScalar
		scalar.SetBytes(&raw)
		matches := signer.key.Key.Equals(&scalar)
		scalar.Zero()
		if !matches {
			return nil, errors.New("no valid chat identity for relay authentication")
		}
		return signer, nil
	}
	signer, err := newSessionSigner(key)
	if err != nil || signer.pubKey != pubkey {
		return nil, errors.New("no valid chat identity for relay authentication")
	}
	if a.signers == nil {
		a.signers = make(map[string]*sessionSigner)
	}
	a.signers[pubkey] = signer
	return signer, nil
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
	for pubkey := range a.signers {
		if _, needed := keys[pubkey]; !needed {
			delete(a.signers, pubkey)
		}
	}
}

func relayRejection(prefix, reason string) error {
	return fmt.Errorf("%s: %s", prefix, truncateString(sanitizeString(reason), 200))
}
