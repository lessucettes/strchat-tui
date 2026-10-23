// Copyright (c) 2026 strchat-tui contributors
//
// SPDX-License-Identifier: BSD-3-Clause

package secp256k1

import (
	"crypto/rand"
	"math/big"
	"testing"
)

func randScalar(t testing.TB) *Scalar {
	t.Helper()
	var raw [ScalarSize]byte
	for {
		if _, err := rand.Read(raw[:]); err != nil {
			t.Fatal(err)
		}
		if s, err := new(Scalar).SetCanonicalBytes(&raw); err == nil {
			return s
		}
	}
}

// A positive digit can carry beyond bit 127 even though the recoded input fits
// in 128 bits; the high word must be shifted in rather than discarded.
func TestWNAFRecodeBoundaryCarry(t *testing.T) {
	top := new(big.Int).Lsh(big.NewInt(1), 128)
	for delta := int64(1); delta <= 64; delta++ {
		want := new(big.Int).Sub(top, big.NewInt(delta))
		var raw [16]byte
		want.FillBytes(raw[:])
		var digits [131]int8
		n := wnafRecode128(raw[:], digits[:])
		got := new(big.Int)
		for j := n - 1; j >= 0; j-- {
			got.Lsh(got, 1)
			got.Add(got, big.NewInt(int64(digits[j])))
		}
		if got.Cmp(want) != 0 {
			t.Fatalf("128-bit top minus %d: reconstructed %s, want %s", delta, got, want)
		}
	}
}

func TestWNAFRecodeRoundTrip(t *testing.T) {
	var digits [131]int8
	for i := 0; i < 2000; i++ {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			t.Fatal(err)
		}
		n := wnafRecode128(raw[:], digits[:])
		want := new(big.Int).SetBytes(raw[:])
		got := new(big.Int)
		for j := n - 1; j >= 0; j-- {
			got.Lsh(got, 1)
			got.Add(got, big.NewInt(int64(digits[j])))
		}
		if got.Cmp(want) != 0 {
			t.Fatalf("recode mismatch: got %s want %s", got, want)
		}
		for j := 0; j < n; j++ {
			if digits[j] != 0 && digits[j]&1 == 0 {
				t.Fatalf("even digit %d", digits[j])
			}
		}
	}
}

// TestWNAFMatchesGLV checks the cached multiply against the incumbent
// per-call GLV multiply on unrelated random points and scalars.
func TestWNAFMatchesGLV(t *testing.T) {
	for i := 0; i < 64; i++ {
		p := newRcvr().scalarBaseMultVartime(randScalar(t))
		s := randScalar(t)
		var tbl WNAFPointTable
		tbl.Build(p)
		got := newRcvr().ScalarMultVartimeWNAF(s, &tbl)
		want := newRcvr().scalarMultVartimeGLV(s, p)
		if got.Equal(want) != 1 {
			t.Fatalf("iteration %d: wNAF result differs from GLV", i)
		}
	}
}

func BenchmarkWNAFTableBuild(b *testing.B) {
	p := newRcvr().scalarBaseMultVartime(randScalar(b))
	b.ReportAllocs()
	var tbl WNAFPointTable
	for b.Loop() {
		tbl.Build(p)
	}
}
