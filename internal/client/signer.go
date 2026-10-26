package client

import (
	"encoding/hex"
	"errors"

	"fiatjaf.com/nostr"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

var errInvalidSignerKey = errors.New("invalid signing key")

// sessionSigner is an immutable, per-chat signing identity. The private key is
// parsed and its public key derived exactly once; queued publishes and relay
// AUTH snapshots capture this value instead of re-deriving a key per event.
//
// The key is never zeroed or mutated on leave: jobs already authorized with it
// keep a valid reference until they finish, preserving captured-identity
// semantics. No secret is persisted or logged.
type sessionSigner struct {
	key    *btcec.PrivateKey
	pubKey nostr.PubKey // derived x-only key of key
}

func newSessionSigner(privKey string) (*sessionSigner, error) {
	raw, err := hex.DecodeString(privKey)
	if err != nil || len(raw) != 32 {
		return nil, errInvalidSignerKey
	}
	key, pub := btcec.PrivKeyFromBytes(raw)
	if key == nil || pub == nil || key.Key.IsZero() {
		return nil, errInvalidSignerKey
	}
	return &sessionSigner{key: key, pubKey: nostr.PubKey(pub.SerializeCompressed()[1:])}, nil
}

// sign mirrors nostr.Event.Sign's observable behavior (PubKey/ID/Sig fields,
// nil tags normalized to empty, deterministic FastSign nonce) while reusing the
// parsed key instead of decoding and deriving it again. It is safe for
// concurrent callers sharing one immutable session: btcec copies the scalar
// before signing and never mutates the PrivateKey.
func (s *sessionSigner) sign(ev *nostr.Event) error {
	if ev.Tags == nil {
		ev.Tags = make(nostr.Tags, 0)
	}
	ev.PubKey = s.pubKey
	ev.ID = canonicalEventID(ev)
	sig, err := schnorr.Sign(s.key, ev.ID[:], schnorr.FastSign())
	if err != nil {
		return err
	}
	ev.Sig = [64]byte(sig.Serialize())
	return nil
}

// signingIdentity validates a chat session's key against its recorded public
// key. Sessions created before this type existed (and table-driven tests) build
// the signer lazily; production identities store it at creation.
func (s chatSession) signingIdentity() (*sessionSigner, error) {
	if s.privKey == "" {
		return nil, errInvalidSignerKey
	}
	signer := s.signer
	if signer == nil {
		var err error
		signer, err = newSessionSigner(s.privKey)
		if err != nil {
			return nil, err
		}
	}
	if signer.pubKey.Hex() != s.pubKey {
		return nil, errors.New("signing identity public key mismatch")
	}
	return signer, nil
}
