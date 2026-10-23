// Copyright (c) 2026 strchat-tui contributors
//
// SPDX-License-Identifier: BSD-3-Clause
//
// Local addition to secp256k1-voi (not upstream): BIP-340 verification with a
// caller-supplied, key-bound table. The table's owner label is derived inside
// this package from the deserialized key, so it cannot be supplied or forged by
// a caller.

package bitcoin

import (
	"bytes"

	secp256k1 "gitlab.com/yawning/secp256k1-voi"
)

// AuthorVerifyTable is an immutable wNAF precomputation bound to one BIP-340
// public key. Its fields are unexported: the owner label comes from the key
// bytes at construction, never from the caller. It may be reused for any
// number of signatures by that key and must not be mutated afterwards.
type AuthorVerifyTable struct {
	owner [secp256k1.CoordSize]byte
	table secp256k1.WNAFPointTable
}

// NewAuthorVerifyTable builds the immutable table for k. The build performs
// point arithmetic only; it never reads or derives secret material.
// k must be a non-nil key returned by a SchnorrPublicKey constructor.
func NewAuthorVerifyTable(k *SchnorrPublicKey) *AuthorVerifyTable {
	t := new(AuthorVerifyTable)
	copy(t.owner[:], k.xBytes)
	t.table.Build(k.point)
	return t
}

// VerifyWithTable verifies sig over msg using t, which must have been built
// for k. The BIP-340 checks and the cheapest parse steps are identical to
// Verify; only the variable-point multiplication uses the precomputed table.
// A table for a different key is rejected before any curve arithmetic.
func (k *SchnorrPublicKey) VerifyWithTable(msg, sig []byte, t *AuthorVerifyTable) bool {
	if len(sig) != SchnorrSignatureSize || t == nil {
		return false
	}
	if !bytes.Equal(t.owner[:], k.xBytes) {
		return false
	}

	ok, s, e, sigRXBytes := parseSchnorrSignature(k.xBytes, msg, sig)
	if !ok {
		return false
	}

	e.Negate(e)
	R := secp256k1.NewIdentityPoint().DoubleScalarMultBasepointVartimeWNAF(s, e, &t.table)

	return verifySchnorrSignatureR(sigRXBytes, R)
}
