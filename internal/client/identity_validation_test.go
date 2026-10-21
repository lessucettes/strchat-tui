package client

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

func TestProcessEventRejectsNonCanonicalIdentity(t *testing.T) {
	c, events := testClient(t)
	event := signedEvent(t, "blocked sender")
	c.config.BlockedUsers = []blockedUser{{PubKey: event.PubKey}}

	// Sign the actual uppercase wire representation: simply changing the case
	// after Sign would invalidate the ID and would not exercise the bypass.
	event.PubKey = strings.ToUpper(event.PubKey)
	hash := sha256.Sum256(event.Serialize())
	keyBytes, err := hex.DecodeString(transportTestKey)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := btcec.PrivKeyFromBytes(keyBytes)
	sig, err := schnorr.Sign(key, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	event.ID = hex.EncodeToString(hash[:])
	event.Sig = hex.EncodeToString(sig.Serialize())
	if valid, err := event.CheckSignature(); !valid || err != nil || !event.CheckID() {
		t.Fatalf("invalid probe signature: %v", err)
	}
	c.processEvent(event, "local")
	if len(events) != 0 || c.userContext.Len() != 0 {
		t.Fatal("non-canonical identity bypassed the block list")
	}
}
