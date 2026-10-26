package client

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"testing"

	"fiatjaf.com/nostr"
	"gitlab.com/yawning/secp256k1-voi/secec/bitcoin"
)

// referenceEventValid is the upstream meaning of a fully valid event: the
// stored ID equals the canonical body hash and the signature matches that body.
// It is the differential oracle for the local verification path.
func referenceEventValid(ev *nostr.Event) bool {
	return ev.CheckID() && ev.VerifySignature()
}

func parseTest32Hex(t testing.TB, hexValue string) [32]byte {
	t.Helper()
	raw, err := hex.DecodeString(hexValue)
	if err != nil || len(raw) != 32 {
		t.Fatalf("invalid 32-byte hex fixture %q: %v (len %d)", hexValue, err, len(raw))
	}
	return [32]byte(raw)
}

func randomSignedEvent(t *testing.T, key string, rng *randReader) *nostr.Event {
	t.Helper()
	ev := &nostr.Event{
		Kind:      ephChatKind,
		CreatedAt: nostr.Timestamp(1700000000 + rng.intn(100000)),
		Content:   rng.text(0, 200),
		Tags:      nostr.Tags{{"d", "lobby"}},
	}
	if err := ev.Sign(parseTest32Hex(t, key)); err != nil {
		t.Fatal(err)
	}
	return ev
}

// randReader is a tiny deterministic test source; crypto/rand supplies the
// bytes so fixtures stay independent of a global math/rand seed.
type randReader struct{ buf [512]byte }

func (r *randReader) intn(n int) int {
	if n <= 0 {
		return 0
	}
	if _, err := rand.Read(r.buf[:1]); err != nil {
		panic(err)
	}
	return int(r.buf[0]) % n
}

func (r *randReader) text(min, max int) string {
	n := min + r.intn(max-min+1)
	if n == 0 {
		return ""
	}
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	for i := range raw {
		raw[i] = 'a' + raw[i]%26
	}
	return string(raw)
}

func TestVerifyEventDigestMatchesReferenceChecks(t *testing.T) {
	keys := []string{
		strings.Repeat("0", 63) + "1",
		"B7E151628AED2A6ABF7158809CF4F3C762E7160F38B4DA56A784D9045190CFEF",
		"FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364140",
	}
	rng := &randReader{}
	for ki, key := range keys {
		for i := range 8 {
			base := randomSignedEvent(t, key, rng)
			other := randomSignedEvent(t, keys[(ki+1)%len(keys)], rng)
			mutations := []struct {
				name   string
				mutate func(*nostr.Event)
			}{
				{"valid", func(*nostr.Event) {}},
				{"flipped-id", func(e *nostr.Event) { e.ID[0] ^= 0xff }},
				// The signature still matches the body, but the stored ID names
				// another event. Upstream VerifySignature ignores the ID, so
				// the local path must reject this itself.
				{"foreign-id", func(e *nostr.Event) { e.ID = other.ID }},
				{"flipped-sig", func(e *nostr.Event) { e.Sig[0] ^= 0xff }},
				{"zero-sig", func(e *nostr.Event) { e.Sig = [64]byte{} }},
				{"corrupt-content", func(e *nostr.Event) { e.Content += "x" }},
				{"other-pubkey", func(e *nostr.Event) { e.PubKey = other.PubKey }},
			}
			for _, m := range mutations {
				ev := *base
				ev.Tags = append(nostr.Tags(nil), base.Tags...)
				m.mutate(&ev)
				want := referenceEventValid(&ev)
				_, _, got := verifyEventDigest(&ev, nil)
				if got != want {
					t.Fatalf("key %d event %d %s: digest path=%t reference=%t (id=%s)", ki, i, m.name, got, want, ev.ID.Hex())
				}
			}
		}
	}
}

// TestVerifyEventDigestRejectsInvalidCurveData covers typed events whose bytes
// are structurally complete but cannot verify: an invalid public point or
// signature scalar. Malformed hex, length and case are wire-decoder concerns
// and are covered by decodeRelayEvent tests, not by impossible typed values.
func TestVerifyEventDigestRejectsInvalidCurveData(t *testing.T) {
	base := randomSignedEvent(t, strings.Repeat("0", 63)+"1", &randReader{})
	fieldP := parseTest32Hex(t, "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEFFFFFC2F")
	orderN := parseTest32Hex(t, "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141")
	cases := []struct {
		name   string
		mutate func(*nostr.Event)
	}{
		{"zero-pubkey", func(e *nostr.Event) {
			e.PubKey = nostr.PubKey{}
			e.ID = e.GetID() // reach the point parse instead of failing the ID check
		}},
		{"field-p-pubkey", func(e *nostr.Event) {
			e.PubKey = nostr.PubKey(fieldP)
			e.ID = e.GetID()
		}},
		{"zero-sig", func(e *nostr.Event) { e.Sig = [64]byte{} }},
		{"r-equals-p", func(e *nostr.Event) { copy(e.Sig[:32], fieldP[:]) }},
		{"s-equals-n", func(e *nostr.Event) { copy(e.Sig[32:], orderN[:]) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := *base
			tc.mutate(&ev)
			if _, _, ok := verifyEventDigest(&ev, nil); ok {
				t.Fatal("invalid curve data verified")
			}
		})
	}
	// A 32-byte all-zero x-only key is not a valid curve point; the official
	// vector set covers the field-size boundary, this covers the common zero.
	if _, err := bitcoin.NewSchnorrPublicKey(make([]byte, 32)); err == nil {
		t.Fatal("zero x-only key parsed as a valid point")
	}
}

func TestAuthorKeyCacheBindingAndTamperRejection(t *testing.T) {
	rng := &randReader{}
	keyA := strings.Repeat("0", 63) + "1"
	keyB := "B7E151628AED2A6ABF7158809CF4F3C762E7160F38B4DA56A784D9045190CFEF"
	evA := randomSignedEvent(t, keyA, rng)
	evB := randomSignedEvent(t, keyB, rng)

	authorA, digestA, ok := verifyEventDigest(evA, nil)
	if !ok {
		t.Fatal("event A did not verify")
	}
	authorB, _, ok := verifyEventDigest(evB, nil)
	if !ok {
		t.Fatal("event B did not verify")
	}
	// Reusing A's cached key for B must re-parse B rather than verify with A.
	gotB, digestB, ok := verifyEventDigest(evB, authorA)
	if !ok || gotB == authorA || gotB.pubKey != evB.PubKey {
		t.Fatal("cached author key was reused for a different author")
	}
	if digestA == digestB {
		t.Fatal("distinct events produced the same digest")
	}

	// A forged binding (B's public key label on A's point) must still fail the
	// signature equation, so cache metadata alone can never authorize an event.
	forged := &authorPublicKey{pubKey: evB.PubKey, key: authorA.key}
	if _, _, ok := verifyEventDigest(evB, forged); ok {
		t.Fatal("signature verified with an unrelated cached point")
	}

	// A tampered event must fail even with its own correctly cached key.
	for _, mutate := range []func(*nostr.Event){
		func(e *nostr.Event) { e.Content += "x" },
		func(e *nostr.Event) { e.Sig[0] ^= 0xff },
		func(e *nostr.Event) { e.ID[0] ^= 0xff },
	} {
		bad := *evB
		mutate(&bad)
		if _, _, ok := verifyEventDigest(&bad, authorB); ok {
			t.Fatal("tampered event verified through the author cache")
		}
	}
}

func TestVerifyEventDigestConcurrent(t *testing.T) {
	rng := &randReader{}
	key := strings.Repeat("0", 63) + "1"
	ev := randomSignedEvent(t, key, rng)
	author, _, ok := verifyEventDigest(ev, nil)
	if !ok {
		t.Fatal("fixture did not verify")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 16 {
				if _, _, ok := verifyEventDigest(ev, author); !ok {
					errs <- fmt.Errorf("cached verification failed")
					return
				}
				cold := *ev
				if _, _, ok := verifyEventDigest(&cold, nil); !ok {
					errs <- fmt.Errorf("cold verification failed")
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}
