package client

import (
	"crypto/sha256"

	"fiatjaf.com/nostr"
)

// canonicalEventID uses the library's streaming hash for ordinary messages.
// The pinned library does not escape uncommon JSON control bytes in Serialize
// or GetID. Keep those valid NIP-01 inputs interoperable until upstream fixes it.
func canonicalEventID(event *nostr.Event) nostr.ID {
	if hasUnescapedControl(event.Content) {
		return sha256.Sum256(serializeCanonicalEvent(event))
	}
	for _, tag := range event.Tags {
		for _, value := range tag {
			if hasUnescapedControl(value) {
				return sha256.Sum256(serializeCanonicalEvent(event))
			}
		}
	}
	return event.GetID()
}

func hasUnescapedControl(s string) bool {
	for i := range len(s) {
		if s[i] < 0x20 && s[i] != '\n' && s[i] != '\r' && s[i] != '\t' {
			return true
		}
	}
	return false
}

// serializeCanonicalEvent repairs only raw control bytes in the library's
// canonical serialization. Quotes, backslashes, Unicode and already-escaped
// tabs/newlines are left intact; blindly replacing escape sequences would alter
// literal backslash text. Mining and signing must hash the same representation.
func serializeCanonicalEvent(event *nostr.Event) []byte {
	serialized := event.Serialize()
	for i, b := range serialized {
		if b >= 0x20 {
			continue
		}
		out := make([]byte, 0, len(serialized)+16)
		out = append(out, serialized[:i]...)
		const hex = "0123456789abcdef"
		for _, b := range serialized[i:] {
			switch {
			case b == '\b':
				out = append(out, '\\', 'b')
			case b == '\f':
				out = append(out, '\\', 'f')
			case b < 0x20:
				out = append(out, '\\', 'u', '0', '0', hex[b>>4], hex[b&15])
			default:
				out = append(out, b)
			}
		}
		return out
	}
	return serialized
}
