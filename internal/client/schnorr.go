package client

import (
	"crypto/sha256"

	"github.com/nbd-wtf/go-nostr"
	"gitlab.com/yawning/secp256k1-voi/secec/bitcoin"
)

// authorPublicKey binds a parsed BIP-340 public key to the exact lowercase
// event key it was derived from. The binding means a bounded cache entry can
// never be used to verify a different author, even if a lookup is mistaken.
type authorPublicKey struct {
	pubKey string
	key    *bitcoin.SchnorrPublicKey
}

// digestMatchesID reports exact lowercase NIP-01 ID equality without
// materializing a second hex string. go-nostr's CheckID uses the same table
// comparison, so uppercase and non-hex IDs remain rejected.
func digestMatchesID(digest [32]byte, id string) bool {
	if len(id) != 64 {
		return false
	}
	const hextable = "0123456789abcdef"
	for i := range 32 {
		if hextable[digest[i]>>4] != id[i*2] || hextable[digest[i]&0x0f] != id[i*2+1] {
			return false
		}
	}
	return true
}

// decodeFixedHex decodes exactly len(dst) bytes from hex without allocating.
// Both hex cases are accepted to preserve go-nostr's accepted signature input.
func decodeFixedHex(dst []byte, src string) bool {
	if len(src) != len(dst)*2 {
		return false
	}
	for i := range dst {
		hi, ok := hexNibble(src[i*2])
		if !ok {
			return false
		}
		lo, ok := hexNibble(src[i*2+1])
		if !ok {
			return false
		}
		dst[i] = hi<<4 | lo
	}
	return true
}

func hexNibble(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// verifyEventDigest hashes the authoritative canonical NIP-01 serialization
// once, checks the exact ID and BIP-340 signature, and returns the parsed
// author key so the owner loop can keep it in a bounded cache. The digest is
// also reused by the optional PoW bit check.
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
	digest := sha256.Sum256(ev.Serialize())
	if !digestMatchesID(digest, ev.ID) {
		return nil, zero, false
	}
	var sig [64]byte
	if !decodeFixedHex(sig[:], ev.Sig) {
		return nil, zero, false
	}
	key := cached
	if key == nil || key.pubKey != ev.PubKey {
		var raw [32]byte
		if !decodeFixedHex(raw[:], ev.PubKey) {
			return nil, zero, false
		}
		parsed, err := bitcoin.NewSchnorrPublicKey(raw[:])
		if err != nil {
			return nil, zero, false
		}
		key = &authorPublicKey{pubKey: ev.PubKey, key: parsed}
	}
	if table := warm.table(ev.PubKey); table != nil {
		if !key.key.VerifyWithTable(digest[:], sig[:], table) {
			return nil, zero, false
		}
		return key, digest, true
	}
	if !key.key.Verify(digest[:], sig[:]) {
		return nil, zero, false
	}
	warm.retain(ev.PubKey, key.key)
	return key, digest, true
}

// cachedAuthorKey returns a previously verified author key for pubkey. The
// cache is the existing bounded owner-only userContext LRU, keyed by the exact
// validated event public key.
func (c *client) cachedAuthorKey(pubkey string) *authorPublicKey {
	if user, ok := c.userContext.Peek(pubkey); ok {
		return user.authorKey
	}
	return nil
}
