package client

import (
	"strconv"
	"strings"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip13"
)

// isPoWValidDigest is isPoWValid with the canonical digest that ID equality and
// signature verification already computed. Callers must have validated the ID
// against the same digest first, so no unverified event reaches this check.
func isPoWValidDigest(event *nostr.Event, minDifficulty int, digest [32]byte) bool {
	if minDifficulty <= 0 {
		return true
	}
	nonceTag := event.Tags.FindLast("nonce")
	if len(nonceTag) < 3 {
		return false
	}
	claimedDifficulty, err := strconv.Atoi(strings.TrimSpace(nonceTag[2]))
	if err != nil || claimedDifficulty < minDifficulty {
		return false
	}
	return nip13.Difficulty(digest) >= claimedDifficulty
}
