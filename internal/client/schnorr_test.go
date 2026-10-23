package client

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"gitlab.com/yawning/secp256k1-voi/secec/bitcoin"
)

func legacyEventValid(ev *nostr.Event) bool {
	if !ev.CheckID() {
		return false
	}
	ok, _ := ev.CheckSignature()
	return ok
}

func randomSignedEvent(t *testing.T, key string, rng *randReader) *nostr.Event {
	t.Helper()
	ev := &nostr.Event{
		Kind:      ephChatKind,
		CreatedAt: nostr.Timestamp(1700000000 + rng.intn(100000)),
		Content:   rng.text(0, 200),
		Tags:      nostr.Tags{{"d", "lobby"}},
	}
	if err := ev.Sign(key); err != nil {
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

func flipHex(s string) string {
	if s == "" {
		return "0"
	}
	i := len(s) / 3
	c := s[i]
	repl := byte('0')
	if c == '0' {
		repl = '1'
	}
	return s[:i] + string(repl) + s[i+1:]
}

func TestVerifyEventDigestMatchesLegacyChecks(t *testing.T) {
	keys := []string{
		strings.Repeat("0", 63) + "1",
		"B7E151628AED2A6ABF7158809CF4F3C762E7160F38B4DA56A784D9045190CFEF",
		"FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364140",
	}
	rng := &randReader{}
	for ki, key := range keys {
		for i := range 8 {
			base := randomSignedEvent(t, key, rng)
			mutations := []struct {
				name   string
				mutate func(*nostr.Event)
			}{
				{"valid", func(*nostr.Event) {}},
				{"uppercase-id", func(e *nostr.Event) { e.ID = strings.ToUpper(e.ID) }},
				{"bad-id", func(e *nostr.Event) { e.ID = flipHex(e.ID) }},
				{"short-id", func(e *nostr.Event) { e.ID = e.ID[:62] }},
				{"bad-sig", func(e *nostr.Event) { e.Sig = flipHex(e.Sig) }},
				{"uppercase-sig", func(e *nostr.Event) { e.Sig = strings.ToUpper(e.Sig) }},
				{"zero-sig", func(e *nostr.Event) { e.Sig = strings.Repeat("0", 128) }},
				{"corrupt-content", func(e *nostr.Event) { e.Content += "x" }},
				{"corrupt-pubkey", func(e *nostr.Event) { e.PubKey = flipHex(e.PubKey) }},
			}
			for _, m := range mutations {
				ev := *base
				ev.Tags = append(nostr.Tags(nil), base.Tags...)
				m.mutate(&ev)
				want := legacyEventValid(&ev)
				_, _, got := verifyEventDigest(&ev, nil)
				if got != want {
					t.Fatalf("key %d event %d %s: digest path=%t legacy=%t (id=%s)", ki, i, m.name, got, want, ev.ID)
				}
			}
		}
	}
}

func TestVerifyEventDigestRejectsMalformedInputs(t *testing.T) {
	key := strings.Repeat("0", 63) + "1"
	base := randomSignedEvent(t, key, &randReader{})
	cases := []struct {
		name   string
		mutate func(*nostr.Event)
	}{
		{"zero-pubkey", func(e *nostr.Event) { e.PubKey = strings.Repeat("0", 64) }},
		{"field-p-pubkey", func(e *nostr.Event) { e.PubKey = "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEFFFFFC2F" }},
		{"uppercase-pubkey", func(e *nostr.Event) { e.PubKey = strings.ToUpper(e.PubKey) }},
		{"nonhex-pubkey", func(e *nostr.Event) { e.PubKey = strings.Repeat("z", 64) }},
		{"empty-pubkey", func(e *nostr.Event) { e.PubKey = "" }},
		{"r-equals-p", func(e *nostr.Event) { e.Sig = "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEFFFFFC2F" + e.Sig[64:] }},
		{"s-equals-n", func(e *nostr.Event) { e.Sig = e.Sig[:64] + "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141" }},
		{"nonhex-sig", func(e *nostr.Event) { e.Sig = strings.Repeat("g", 128) }},
		{"short-sig", func(e *nostr.Event) { e.Sig = e.Sig[:126] }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := *base
			tc.mutate(&ev)
			if _, _, ok := verifyEventDigest(&ev, nil); ok {
				t.Fatal("malformed event verified")
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
		func(e *nostr.Event) { e.Sig = flipHex(e.Sig) },
		func(e *nostr.Event) { e.ID = flipHex(e.ID) },
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

func TestDecodeFixedHexMatchesEncodingHex(t *testing.T) {
	rng := &randReader{}
	for range 500 {
		raw := make([]byte, 32+rng.intn(33))
		if _, err := rand.Read(raw); err != nil {
			t.Fatal(err)
		}
		encoded := hex.EncodeToString(raw)
		if rng.intn(2) == 0 {
			encoded = strings.ToUpper(encoded)
		}
		decoded := make([]byte, len(raw))
		if !decodeFixedHex(decoded, encoded) {
			t.Fatalf("failed to decode %q", encoded)
		}
		if !equalBytes(decoded, raw) {
			t.Fatalf("decoded %x, want %x", decoded, raw)
		}
		if decodeFixedHex(decoded, encoded[:len(encoded)-2]) {
			t.Fatal("accepted a short encoding")
		}
	}
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
