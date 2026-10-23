package client

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/nbd-wtf/go-nostr"
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
	pubKey string // lowercase x-only hex, derived from key
}

func newSessionSigner(privKey string) (*sessionSigner, error) {
	var raw [32]byte
	if !decodeFixedHex(raw[:], privKey) {
		return nil, errInvalidSignerKey
	}
	key, pub := btcec.PrivKeyFromBytes(raw[:])
	if key == nil || pub == nil || key.Key.IsZero() {
		return nil, errInvalidSignerKey
	}
	return &sessionSigner{key: key, pubKey: hex.EncodeToString(schnorr.SerializePubKey(pub))}, nil
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
	digest := sha256.Sum256(ev.Serialize())
	sig, err := schnorr.Sign(s.key, digest[:], schnorr.FastSign())
	if err != nil {
		return err
	}
	ev.ID = hex.EncodeToString(digest[:])
	ev.Sig = hex.EncodeToString(sig.Serialize())
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
	if signer.pubKey != s.pubKey {
		return nil, errors.New("signing identity public key mismatch")
	}
	return signer, nil
}
