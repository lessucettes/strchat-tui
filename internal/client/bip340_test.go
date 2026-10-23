package client

import (
	"encoding/csv"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"gitlab.com/yawning/secp256k1-voi/secec/bitcoin"
)

// TestOfficialBIP340Vectors runs the complete official BIP-340 verification
// vector set (including the 2022 variable-length-message additions) against the
// Pure Go verifier used for inbound events. Vectors whose public key cannot be
// parsed are negative vectors and must fail verification, not abort the test.
func TestOfficialBIP340Vectors(t *testing.T) {
	f, err := os.Open("testdata/bip340-test-vectors.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 20 || strings.TrimSpace(rows[0][0]) != "index" {
		t.Fatalf("unexpected vector file shape: %d rows", len(rows))
	}
	checked := 0
	for _, row := range rows[1:] {
		if len(row) != 8 {
			t.Fatalf("vector row has %d columns, want 8", len(row))
		}
		index := strings.TrimSpace(row[0])
		pubBytes, err := hex.DecodeString(strings.TrimSpace(row[2]))
		if err != nil {
			t.Fatalf("vector %s public key: %v", index, err)
		}
		msg, err := hex.DecodeString(strings.TrimSpace(row[4]))
		if err != nil {
			t.Fatalf("vector %s message: %v", index, err)
		}
		sig, err := hex.DecodeString(strings.TrimSpace(row[5]))
		if err != nil {
			t.Fatalf("vector %s signature: %v", index, err)
		}
		want := strings.TrimSpace(row[6]) == "TRUE"

		valid := false
		if pk, err := bitcoin.NewSchnorrPublicKey(pubBytes); err == nil {
			valid = pk.Verify(msg, sig)
		}
		if valid != want {
			t.Fatalf("vector %s (%s): valid=%t want %t", index, row[7], valid, want)
		}
		checked++
	}
	if checked != 19 {
		t.Fatalf("checked %d vectors, want 19", checked)
	}
}
