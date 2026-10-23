// Copyright (c) 2026 strchat-tui contributors
//
// SPDX-License-Identifier: BSD-3-Clause

package bitcoin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/csv"
	"os"
	"testing"

	secp256k1 "gitlab.com/yawning/secp256k1-voi"
	"gitlab.com/yawning/secp256k1-voi/internal/helpers"
)

func TestVerifyWithTableOfficialVectors(t *testing.T) {
	f, err := os.Open("testdata/bip-0340-test-vectors.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	for i, vec := range records[1:] {
		pk, err := NewSchnorrPublicKey(helpers.MustBytesFromHex(vec[2]))
		if err != nil {
			if vec[6] != "FALSE" {
				t.Fatalf("vector %d: valid-result key failed to parse: %v", i, err)
			}
			continue
		}
		msg := helpers.MustBytesFromHex(vec[4])
		sig := helpers.MustBytesFromHex(vec[5])
		want := vec[6] == "TRUE"
		if got := pk.VerifyWithTable(msg, sig, NewAuthorVerifyTable(pk)); got != want {
			t.Fatalf("vector %d: VerifyWithTable = %t, want %t", i, got, want)
		}
		if got := pk.Verify(msg, sig); got != want {
			t.Fatalf("vector %d: Verify = %t, want %t", i, got, want)
		}
	}
}

func TestVerifyWithTable(t *testing.T) {
	priv, err := GenerateSchnorrKey()
	if err != nil {
		t.Fatal(err)
	}
	pub := priv.PublicKey()
	msg := sha256.Sum256([]byte("wnaf table fixture"))
	sig, err := priv.Sign(rand.Reader, msg[:], nil)
	if err != nil {
		t.Fatal(err)
	}
	if ok := pub.Verify(msg[:], sig); !ok {
		t.Fatal("plain Verify rejected a valid signature")
	}
	tbl := NewAuthorVerifyTable(pub)
	if ok := pub.VerifyWithTable(msg[:], sig, tbl); !ok {
		t.Fatal("VerifyWithTable rejected a valid signature")
	}

	bad := append([]byte(nil), sig...)
	bad[13] ^= 0x01
	if ok := pub.VerifyWithTable(msg[:], bad, tbl); ok {
		t.Fatal("VerifyWithTable accepted a corrupted signature")
	}
	if ok := pub.VerifyWithTable(nil, sig, nil); ok {
		t.Fatal("VerifyWithTable accepted a nil table")
	}
	// An uninitialized table must not authorize either a genuine signature or
	// the identity-table forgery r=x(G), s=1 from the rejected WithOwner API.
	var zero AuthorVerifyTable
	forged := make([]byte, SchnorrSignatureSize)
	x, err := secp256k1.NewGeneratorPoint().XBytes()
	if err != nil {
		t.Fatal(err)
	}
	copy(forged, x[:])
	forged[len(forged)-1] = 1
	if pub.VerifyWithTable(msg[:], sig, &zero) || pub.VerifyWithTable(msg[:], forged, &zero) || pub.VerifyWithTable(msg[:], forged, tbl) {
		t.Fatal("uninitialized or genuine table authorized an identity-table forgery")
	}
	// Keys constructed from the opposite parity normalize to the same even-y
	// point before the owner and table are derived together.
	odd := secp256k1.NewPointFrom(pub.Point())
	odd.Negate(odd)
	fromOdd, err := NewSchnorrPublicKeyFromPoint(odd)
	if err != nil {
		t.Fatal(err)
	}
	if !fromOdd.VerifyWithTable(msg[:], sig, NewAuthorVerifyTable(fromOdd)) {
		t.Fatal("table built from an odd-y source rejected a valid signature")
	}

	other, err := GenerateSchnorrKey()
	if err != nil {
		t.Fatal(err)
	}
	otherTbl := NewAuthorVerifyTable(other.PublicKey())
	if ok := pub.VerifyWithTable(msg[:], sig, otherTbl); ok {
		t.Fatal("VerifyWithTable accepted a table bound to another key")
	}
}

// TestVerifyWithTableDifferential checks the table path against plain Verify
// over unrelated keys, messages and signature mutations.
func TestVerifyWithTableDifferential(t *testing.T) {
	for i := 0; i < 32; i++ {
		priv, err := GenerateSchnorrKey()
		if err != nil {
			t.Fatal(err)
		}
		pub := priv.PublicKey()
		tbl := NewAuthorVerifyTable(pub)
		msg := sha256.Sum256([]byte{byte(i), byte(i >> 8)})
		sig, err := priv.Sign(rand.Reader, msg[:], nil)
		if err != nil {
			t.Fatal(err)
		}
		if !pub.Verify(msg[:], sig) || !pub.VerifyWithTable(msg[:], sig, tbl) {
			t.Fatalf("iteration %d: valid signature rejected", i)
		}
		for bit := 0; bit < 16; bit++ {
			bad := append([]byte(nil), sig...)
			bad[bit] ^= 1 << uint(bit&7)
			want := pub.Verify(msg[:], bad)
			if got := pub.VerifyWithTable(msg[:], bad, tbl); got != want {
				t.Fatalf("iteration %d bit %d: table=%t plain=%t", i, bit, got, want)
			}
			badMsg := msg
			badMsg[bit&31] ^= 1 << uint(bit&7)
			want = pub.Verify(badMsg[:], sig)
			if got := pub.VerifyWithTable(badMsg[:], sig, tbl); got != want {
				t.Fatalf("iteration %d msg-bit %d: table=%t plain=%t", i, bit, got, want)
			}
		}
	}
}
