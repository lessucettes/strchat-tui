// tui/utils.go
package tui

import (
	"crypto/sha256"
	"strings"
)

// extractNickPrefix finds a potential nick prefix (e.g., "@user#1234") at the end of a string.
// It returns the found nick and a boolean indicating if the nick is complete (has a valid tag).
func extractNickPrefix(s string) (nick string, complete bool) {
	lastAt := strings.LastIndexByte(s, '@')
	if lastAt == -1 {
		return "", false
	}

	after := s[lastAt+1:]
	hashIdx := strings.LastIndexByte(after, '#')

	for hashIdx != -1 {
		if hashIdx+5 <= len(after) {
			tag := after[hashIdx+1 : hashIdx+5]
			ok := true
			for j := range 4 {
				c := tag[j]
				if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) {
					ok = false
					break
				}
			}

			if ok && (hashIdx+5 == len(after) || after[hashIdx+5] == ' ') {
				return after[:hashIdx+5], true
			}
		}
		if hashIdx > 0 {
			hashIdx = strings.LastIndexByte(after[:hashIdx], '#')
		} else {
			break
		}
	}

	return after, false
}

// pubkeyToColor selects a color for a pubkey from a given palette.
func pubkeyToColor(pubkey string, palette []string) string {
	if len(palette) == 0 {
		return "[white]" // Fallback
	}
	hash := sha256.Sum256([]byte(pubkey))
	return palette[int(hash[0])%len(palette)]
}
