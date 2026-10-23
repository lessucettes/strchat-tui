// Copyright (c) 2026 strchat-tui contributors
//
// SPDX-License-Identifier: BSD-3-Clause
//
// Local addition to secp256k1-voi (not upstream): cached odd-multiple tables
// with signed-digit (wNAF) recoding for repeated variable-point multiplication.
// Public verification data only; never used with a secret scalar.
//
// The table carries no owner label. Callers that need to bind a table to a
// public key must use secec/bitcoin.AuthorVerifyTable, which derives the binding
// from an already-deserialized key.

package secp256k1

import "math/bits"

// wnafWindow is the signed-digit window width. 5 matches libsecp256k1's
// verification window (8 odd multiples) and keeps the cached table small.
const wnafWindow = 5

const wnafTableSize = 1 << (wnafWindow - 2) // odd multiples 1,3,...,2^(w-1)-1

// WNAFPointTable caches the odd multiples [1P,3P,...,(2^(w-1)-1)P] and the
// corresponding multiples of beta*P. It is a plain value so that the cold path
// can keep it on the stack; the warm path stores an immutable instance.
// It does not bind an external key to a point: authorization callers must use
// secec/bitcoin.AuthorVerifyTable, not a separately supplied key/table pair.
type WNAFPointTable struct {
	pOdd      [wnafTableSize]Point
	pPrimeOdd [wnafTableSize]Point
}

func buildOddTable(dst []Point, p *Point) {
	dst[0].Set(p)
	var twoP Point
	twoP.doubleComplete(p)
	for i := 1; i < len(dst); i++ {
		dst[i].addComplete(&dst[i-1], &twoP)
	}
}

// Build fills t with the odd-multiple tables for p. It performs 2 point
// doublings and 14 complete additions and allocates nothing when t does not
// escape. t must not be used concurrently while Build is running.
func (t *WNAFPointTable) Build(p *Point) {
	buildOddTable(t.pOdd[:], p)
	betaP := newMulBeta(p)
	buildOddTable(t.pPrimeOdd[:], betaP)
}

// addDigit adds (+/-)d * P (or beta*P) to sum, d odd, |d| < 2^(w-1).
func addDigit(sum *Point, tbl *[wnafTableSize]Point, d int8) *Point {
	if d == 0 {
		return sum
	}
	var idx int
	neg := d < 0
	if neg {
		idx = int(-d) >> 1
	} else {
		idx = int(d) >> 1
	}
	if !neg {
		return sum.addComplete(sum, &tbl[idx])
	}
	var addend Point
	addend.x.Set(&tbl[idx].x)
	addend.y.Negate(&tbl[idx].y)
	addend.z.Set(&tbl[idx].z)
	addend.isValid = true
	return sum.addComplete(sum, &addend)
}

// wnafRecode128 writes the signed-digit recoding of the low 128 bits of k
// (big-endian) least-significant digit first, and returns the digit count.
// A positive digit can carry into bit 128, so the high word is shifted in
// through carry128 rather than being discarded.
func wnafRecode128(k []byte, dst []int8) int {
	var hi, lo uint64
	for i := 0; i < 8; i++ {
		hi = hi<<8 | uint64(k[i])
		lo = lo<<8 | uint64(k[i+8])
	}

	const w = wnafWindow
	const mask = uint64(1)<<w - 1
	const half = uint64(1) << (w - 1)

	n := 0
	for hi != 0 || lo != 0 {
		var d int8
		var carry128 uint64
		if lo&1 == 1 {
			d = int8(lo & mask)
			if uint64(d) >= half {
				d -= 1 << w
			}
			if d > 0 {
				var borrow uint64
				lo, borrow = bits.Sub64(lo, uint64(d), 0)
				hi -= borrow
			} else {
				var carry uint64
				lo, carry = bits.Add64(lo, uint64(-d), 0)
				hi, carry128 = bits.Add64(hi, 0, carry)
			}
		}
		dst[n] = d
		n++
		lo = lo>>1 | hi<<63
		hi = hi>>1 | carry128<<63
	}
	return n
}

// ScalarMultVartimeWNAF sets v = s*p using cached odd-multiple tables and
// interleaved wNAF recoding of the GLV halves. tbl must have been built from
// the exact point p. Both the point and scalar must be public verification
// data; this variable-time primitive must never process a secret scalar.
func (v *Point) ScalarMultVartimeWNAF(s *Scalar, tbl *WNAFPointTable) *Point {
	k1, k2 := s.splitGLV()
	neg1 := k1.IsGreaterThanHalfN() == 1
	if neg1 {
		k1.Negate(k1)
	}
	neg2 := k2.IsGreaterThanHalfN() == 1
	if neg2 {
		k2.Negate(k2)
	}

	const off = 16
	k1Bytes, k2Bytes := k1.Bytes(), k2.Bytes()
	k1Bytes, k2Bytes = k1Bytes[off:], k2Bytes[off:]

	var d1, d2 [131]int8
	n1 := wnafRecode128(k1Bytes, d1[:])
	n2 := wnafRecode128(k2Bytes, d2[:])
	n := n1
	if n2 > n {
		n = n2
	}

	v.Identity()
	for i := n - 1; i >= 0; i-- {
		if i != n-1 {
			v.doubleComplete(v)
		}
		if i < n1 && d1[i] != 0 {
			d := d1[i]
			if neg1 {
				d = -d
			}
			addDigit(v, &tbl.pOdd, d)
		}
		if i < n2 && d2[i] != 0 {
			d := d2[i]
			if neg2 {
				d = -d
			}
			addDigit(v, &tbl.pPrimeOdd, d)
		}
	}

	return v
}

// DoubleScalarMultBasepointVartimeWNAF sets v = u1*G + u2*P using a cached
// wNAF table built from exactly P. Both scalars must be public; this routine
// is not a signing primitive and does not authenticate a separate key label.
func (v *Point) DoubleScalarMultBasepointVartimeWNAF(u1, u2 *Scalar, tbl *WNAFPointTable) *Point {
	u1g := newRcvr().scalarBaseMultVartime(u1)
	u2p := newRcvr().ScalarMultVartimeWNAF(u2, tbl)
	return v.Add(u1g, u2p)
}
