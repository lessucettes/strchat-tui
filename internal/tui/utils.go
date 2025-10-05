package tui

import (
	"crypto/sha256"
	"strings"

	"github.com/rivo/uniseg"
)

// extractNickPrefix finds a potential nick prefix (e.g., "@user#1234") at the end of a string.
// It returns the found nick and a boolean indicating if the nick is complete (has a valid tag).
func extractNickPrefix(s string) (nick string, complete bool) {
	lastAt := strings.LastIndex(s, "@")
	if lastAt == -1 {
		return "", false
	}

	after := s[lastAt+1:]
	rs := []rune(after)

	for hashIdx := len(rs) - 1; hashIdx >= 0; hashIdx-- {
		if rs[hashIdx] != '#' {
			continue
		}

		if hashIdx+5 <= len(rs) {
			tagRunes := rs[hashIdx+1 : hashIdx+5]
			ok := true
			for j := range 4 {
				c := tagRunes[j]
				if !((c >= '0' && c <= '9') ||
					(c >= 'A' && c <= 'Z') ||
					(c >= 'a' && c <= 'z')) {
					ok = false
					break
				}
			}

			if ok && (hashIdx+5 == len(rs) || rs[hashIdx+5] == ' ') {
				return string(rs[:hashIdx+5]), true
			}
		}
	}

	return string(rs), false
}

// pubkeyToColor selects a color for a pubkey from a given palette.
func pubkeyToColor(pubkey string, palette []string) string {
	if len(palette) == 0 {
		return "[white]" // Fallback
	}
	hash := sha256.Sum256([]byte(pubkey))
	return palette[int(hash[0])%len(palette)]
}

// graphemeLen counts user-perceived characters (grapheme clusters)
// to handle emoji and ZWJ sequences correctly in TUI input fields.
func graphemeLen(s string) int {
	g := uniseg.NewGraphemes(s)
	count := 0
	for g.Next() {
		count++
	}
	return count
}
