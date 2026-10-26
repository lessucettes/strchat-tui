package client

import (
	"fiatjaf.com/nostr"
	"gitlab.com/yawning/secp256k1-voi/secec/bitcoin"
)

// authorPublicKey binds a parsed BIP-340 public key to the exact typed event
// key it was derived from. The binding means a bounded cache entry can never
// be used to verify a different author, even if a lookup is mistaken.
type authorPublicKey struct {
	pubKey nostr.PubKey
	key    *bitcoin.SchnorrPublicKey
}

// verifyEventDigest hashes the authoritative legacy canonical NIP-01
// serialization once via canonicalEventID, checks exact ID equality and the
// BIP-340 signature, and returns the parsed author key so the owner loop can
// keep it in a bounded cache. The digest is also reused by the optional PoW bit
// check.
//
// Verification uses secp256k1-voi's Pure Go BIP-340 implementation. Public
// verification data may use variable-time math; signing stays on the existing
// btcec implementation (see signer.go).
func verifyEventDigest(ev *nostr.Event, cached *authorPublicKey) (*authorPublicKey, [32]byte, bool) {
	return verifyEventDigestWarm(ev, cached, nil)
}

// verifyEventDigestWarm optionally reuses immutable, key-bound public tables.
// A miss takes the cold stack-built path and retains a table only after the
// signature validates. A nil cache retains nothing and remains fully supported.
func verifyEventDigestWarm(ev *nostr.Event, cached *authorPublicKey, warm *authorVerifyCache) (*authorPublicKey, [32]byte, bool) {
	var zero [32]byte
	// Event.GetID cannot be used directly: the pinned library leaves uncommon
	// control bytes raw in its serialization, which both breaks JSON validity
	// and changes the ID peers signed under the legacy encoder.
	id := canonicalEventID(ev)
	// Event.VerifySignature only proves that a signature matches the event
	// body; it deliberately ignores the wire ID. NIP-01 requires an exact ID,
	// and the typed comparison replaces the old lowercase hex table check.
	if id != ev.ID {
		return nil, zero, false
	}
	digest := [32]byte(id)
	key := cached
	if key == nil || key.pubKey != ev.PubKey {
		parsed, err := bitcoin.NewSchnorrPublicKey(ev.PubKey[:])
		if err != nil {
			return nil, zero, false
		}
		key = &authorPublicKey{pubKey: ev.PubKey, key: parsed}
	}
	if table := warm.table(ev.PubKey); table != nil {
		if !key.key.VerifyWithTable(digest[:], ev.Sig[:], table) {
			return nil, zero, false
		}
		return key, digest, true
	}
	if !key.key.Verify(digest[:], ev.Sig[:]) {
		return nil, zero, false
	}
	warm.retain(ev.PubKey, key.key)
	return key, digest, true
}

// cachedAuthorKey returns a previously verified author key for pubkey. The
// cache is the existing bounded owner-only userContext LRU, keyed by the exact
// validated event public key. The string boundary is the userContext key.
func (c *client) cachedAuthorKey(pubkey string) *authorPublicKey {
	if user, ok := c.userContext.Peek(pubkey); ok {
		return user.authorKey
	}
	return nil
}
