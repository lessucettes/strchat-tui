package tui

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
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

// pubkeyToNickColorTag returns a tview color tag [#rrggbb] unique per pubkey (stable HSV from hash).
func pubkeyToNickColorTag(pubkey string) string {
	if pubkey == "" {
		return "[#aaaaaa]"
	}
	var h uint64 = 14695981039346656037
	for i := 0; i < len(pubkey); i++ {
		h ^= uint64(pubkey[i])
		h *= 1099511628211
	}
	hue := float64(h % 360)
	s := 0.52 + float64((h>>32)&0x1f)/80.0 // 0.52–0.91
	v := 0.80 + float64((h>>40)&0xf)/100.0 // 0.80–0.95
	r, g, b := hsvToRGBBytes(hue, s, v)
	return fmt.Sprintf("[#%02x%02x%02x]", r, g, b)
}

func hsvToRGBBytes(h, s, v float64) (r, g, b int) {
	c := v * s
	hh := math.Mod(h, 360)
	if hh < 0 {
		hh += 360
	}
	x := c * (1 - math.Abs(math.Mod(hh/60, 2)-1))
	m := v - c
	var rp, gp, bp float64
	switch {
	case hh < 60:
		rp, gp, bp = c, x, 0
	case hh < 120:
		rp, gp, bp = x, c, 0
	case hh < 180:
		rp, gp, bp = 0, c, x
	case hh < 240:
		rp, gp, bp = 0, x, c
	case hh < 300:
		rp, gp, bp = x, 0, c
	default:
		rp, gp, bp = c, 0, x
	}
	r = int(math.Round((rp + m) * 255))
	g = int(math.Round((gp + m) * 255))
	b = int(math.Round((bp + m) * 255))
	if r < 0 {
		r = 0
	} else if r > 255 {
		r = 255
	}
	if g < 0 {
		g = 0
	} else if g > 255 {
		g = 255
	}
	if b < 0 {
		b = 0
	} else if b > 255 {
		b = 255
	}
	return r, g, b
}

// isShortPubKeyTag reports whether s is exactly 4 hex-ish chars (short id in @nick#tag).
func isShortPubKeyTag(s string) bool {
	if len(s) != 4 {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsNumber(r) {
			return false
		}
	}
	return true
}

// highlightPlainMentionOfMe wraps plain @myNick (not @myNick#xxxx) in a bold color tag.
func highlightPlainMentionOfMe(content, myNick string, inputColor tcell.Color) string {
	if myNick == "" {
		return content
	}
	sub := "@" + myNick
	var b strings.Builder
	i := 0
	for i < len(content) {
		idx := strings.Index(content[i:], sub)
		if idx < 0 {
			b.WriteString(content[i:])
			break
		}
		idx += i
		b.WriteString(content[i:idx])
		after := idx + len(sub)
		if after < len(content) && content[after] == '#' && after+4 <= len(content) {
			tag := content[after+1 : after+5]
			if isShortPubKeyTag(tag) {
				b.WriteString(content[idx : after+5])
				i = after + 5
				continue
			}
		}
		fmt.Fprintf(&b, "[%s::b]%s[-]", inputColor, sub)
		i = after
	}
	return b.String()
}

// Nicks may be Latin, Cyrillic, toki-pona triads with hyphens, etc.
var nickAtMentionRE = regexp.MustCompile(`@([\p{L}\p{N}_\-]+)#([a-fA-F0-9]{4})`)

// mentionContinuesAfterHash is true if the rune right after @nick#hhhh looks like more id (reject 5+ char ids).
func mentionContinuesAfterHash(s string, end int) bool {
	if end >= len(s) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s[end:])
	if r == utf8.RuneError {
		return false
	}
	if r == '#' || r == '_' {
		return true
	}
	return unicode.IsLetter(r) || unicode.IsNumber(r)
}

// nickHashMentionIndices finds @nick#4char spans (Unicode nicks). Indices are byte offsets.
func nickHashMentionIndices(s string) [][]int {
	var out [][]int
	i := 0
	for i < len(s) {
		loc := nickAtMentionRE.FindStringSubmatchIndex(s[i:])
		if loc == nil {
			break
		}
		for j := range loc {
			loc[j] += i
		}
		fe := loc[1]
		if !mentionContinuesAfterHash(s, fe) {
			out = append(out, append([]int(nil), loc...))
			i = fe
		} else {
			i = loc[0] + 1
		}
	}
	return out
}

func isHexPubPrefixByte(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F'
}

// isSelfMentionedByPubPrefix is true if content has @<nick>#<my4hex> and my4hex matches
// our pubkey prefix (what others see as #xxxx). Works even when UI nick ≠ mention text
// (e.g. "anon" in replies vs toki-pona in state).
func isSelfMentionedByPubPrefix(content, myShortPubKey string) bool {
	sh := strings.ToLower(strings.TrimSpace(myShortPubKey))
	if len(sh) > 4 {
		sh = sh[len(sh)-4:]
	}
	if len(sh) != 4 {
		return false
	}
	for i := 0; i < len(sh); i++ {
		if !isHexPubPrefixByte(sh[i]) {
			return false
		}
	}
	lower := strings.ToLower(content)
	for i := 0; i < len(lower); {
		j := strings.IndexByte(lower[i:], '@')
		if j < 0 {
			break
		}
		at := i + j
		h := strings.IndexByte(lower[at+1:], '#')
		if h < 0 {
			i = at + 1
			continue
		}
		h = at + 1 + h
		nickPart := lower[at+1 : h]
		if nickPart == "" || strings.ContainsAny(nickPart, " \t\n\r") {
			i = at + 1
			continue
		}
		if h+4 > len(lower) {
			i = at + 1
			continue
		}
		idPart := lower[h+1 : h+5]
		if idPart != sh {
			i = at + 1
			continue
		}
		// Require exactly 4 hex id (not @x#74a999).
		if h+5 < len(lower) && isHexPubPrefixByte(lower[h+5]) {
			i = at + 1
			continue
		}
		return true
	}
	return false
}

// isSelfMentionedInContent: @…#myPubPrefix, or exact @myNick#myShortId.
func isSelfMentionedInContent(content, myNick, myShortPubKey string) bool {
	if myShortPubKey != "" && isSelfMentionedByPubPrefix(content, myShortPubKey) {
		return true
	}
	if myNick == "" || myShortPubKey == "" {
		return false
	}
	wantNick := strings.ToLower(myNick)
	sh := strings.ToLower(myShortPubKey)
	if len(sh) > 4 {
		sh = sh[len(sh)-4:]
	}
	if len(sh) != 4 {
		return false
	}
	for _, loc := range nickHashMentionIndices(content) {
		nick := strings.ToLower(content[loc[2]:loc[3]])
		h := strings.ToLower(content[loc[4]:loc[5]])
		if nick == wantNick && h == sh {
			return true
		}
	}
	return false
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
