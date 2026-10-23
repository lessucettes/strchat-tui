package client

import (
	"math/bits"
	"strconv"
	"strings"

	"github.com/nbd-wtf/go-nostr"
)

// countLeadingZeroBitsDigest is the byte-oriented equivalent of
// countLeadingZeroBits applied to the canonical event ID hex.
func countLeadingZeroBitsDigest(digest [32]byte) int {
	count := 0
	for _, b := range digest {
		if b == 0 {
			count += 8
			continue
		}
		return count + bits.LeadingZeros8(b)
	}
	return count
}

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
	return countLeadingZeroBitsDigest(digest) >= claimedDifficulty
}
