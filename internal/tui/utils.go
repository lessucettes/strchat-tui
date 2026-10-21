package tui

import (
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

		// Normal completions use four characters; collisions use the full key.
		for _, tagLen := range []int{4, 64} {
			end := hashIdx + 1 + tagLen
			if end > len(rs) || (end < len(rs) && rs[end] != ' ') {
				continue
			}
			tagRunes := rs[hashIdx+1 : end]
			ok := true
			for _, c := range tagRunes {
				if !((c >= '0' && c <= '9') ||
					(c >= 'A' && c <= 'Z') ||
					(c >= 'a' && c <= 'z')) {
					ok = false
					break
				}
			}

			if ok {
				return string(rs[:end]), true
			}
		}
	}

	return string(rs), false
}

// pubkeyToColor selects a color for a pubkey from a given palette.
func pubkeyToColor(pubkey string, palette []string) string {
	var sum byte
	for i := 0; i < len(pubkey); i += 2 {
		sum ^= pubkey[i]
	}
	return palette[int(sum)%len(palette)]
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
