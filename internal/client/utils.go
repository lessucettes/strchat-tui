package client

import (
	"fmt"
	"math/bits"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/clipperhouse/uax29/graphemes"
	"github.com/nbd-wtf/go-nostr"
	"github.com/rivo/uniseg"
)

var hexToLeadingZeros [256]int

func init() {
	for i := range 256 {
		char := byte(i)
		var val uint64
		if char >= '0' && char <= '9' {
			val, _ = strconv.ParseUint(string(char), 16, 4)
		} else if char >= 'a' && char <= 'f' {
			val, _ = strconv.ParseUint(string(char), 16, 4)
		} else if char >= 'A' && char <= 'F' {
			val, _ = strconv.ParseUint(string(char), 16, 4)
		} else {
			hexToLeadingZeros[i] = -1
			continue
		}
		if val == 0 {
			hexToLeadingZeros[i] = 4
		} else {
			hexToLeadingZeros[i] = bits.LeadingZeros8(uint8(val << 4))
		}
	}
}

func countLeadingZeroBits(hexString string) int {
	count := 0
	for i := 0; i < len(hexString); i++ {
		char := hexString[i]
		zeros := hexToLeadingZeros[char]

		if zeros == -1 {
			return count
		}

		count += zeros
		if zeros != 4 {
			break
		}
	}
	return count
}

func isPoWValid(event *nostr.Event, minDifficulty int) bool {
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

	actualDifficulty := countLeadingZeroBits(event.ID)
	return actualDifficulty >= claimedDifficulty
}

func safeSuffix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// truncateString uses Unicode 15.0 grapheme boundaries, matching uniseg v0.4.7.
// Only boundary positions are needed here, not uniseg's width/line-break data.
func truncateString(s string, maxClusters int) string {
	// Every cluster occupies at least one byte. Most messages need no scan.
	if len(s) <= maxClusters || s == "" {
		return s
	}
	if !utf8.ValidString(s) {
		return truncateStringUniseg(s, maxClusters)
	}
	count := 0
	if s[0] >= ' ' && s[0] <= '~' {
		// Leave the last ASCII byte: a following mark may extend its cluster.
		for count < maxClusters && count+1 < len(s) && s[count+1] >= ' ' && s[count+1] <= '~' {
			count++
		}
	}
	prefix, end := count, count
	it := graphemes.NewStringSegmenter(s[prefix:])
	for count < maxClusters && it.Next() {
		end = prefix + it.End()
		count++
	}
	if it.Err() != nil {
		return truncateStringUniseg(s, maxClusters)
	}
	if end == len(s) {
		return s
	}
	return s[:end] + "..."
}

func truncateStringUniseg(s string, maxClusters int) string {
	// Every cluster occupies at least one byte. Most messages need no scan.
	if len(s) <= maxClusters || s == "" {
		return s
	}
	rest, state := s, -1
	count := 0
	if rest[0] >= ' ' && rest[0] <= '~' {
		// Skip a printable ASCII prefix, leaving its last byte in case a
		// following Unicode mark extends that cluster. Keep this outside the
		// Unicode loop so mixed text pays no extra per-cluster branch.
		for count < maxClusters && count+1 < len(s) && s[count+1] >= ' ' && s[count+1] <= '~' {
			count++
		}
		rest = rest[count:]
	}
	for ; count < maxClusters && rest != ""; count++ {
		_, rest, _, state = uniseg.FirstGraphemeClusterInString(rest, state)
	}
	if rest == "" {
		return s
	}
	return s[:len(s)-len(rest)] + "..."
}

func normalizeAndValidateChatName(name string) (string, error) {
	normalized := strings.ToLower(name)
	var builder strings.Builder
	builder.Grow(len(normalized))
	var lastWasDash bool
	for _, r := range normalized {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			builder.WriteRune(r)
			lastWasDash = false
		} else if unicode.IsSpace(r) || r == '-' {
			if !lastWasDash {
				builder.WriteRune('-')
				lastWasDash = true
			}
		} else {
			return "", fmt.Errorf("chat name contains invalid character: '%c'", r)
		}
	}
	return strings.Trim(builder.String(), "-"), nil
}

func sanitizeString(s string) string {
	for i, r := range s {
		invalid := false
		if r == utf8.RuneError {
			_, size := utf8.DecodeRuneInString(s[i:])
			invalid = size == 1
		}
		if (r >= 32 || r == '\n' || r == '	') && r != 127 && !invalid {
			continue
		}
		// Allocate only after the first removed control or invalid UTF-8 byte.
		// WriteRune retains the old replacement-rune behavior for invalid input.
		var b strings.Builder
		b.Grow(len(s))
		b.WriteString(s[:i])
		for _, r := range s[i:] {
			if r < 32 && r != '\n' && r != '	' || r == 127 {
				continue
			}
			b.WriteRune(r)
		}
		return b.String()
	}
	return s
}

func normalizeRelayURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		raw = "wss://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid relay URL")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "wss" && u.Scheme != "ws" {
		return "", fmt.Errorf("relay URL must use ws:// or wss://")
	}
	if u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return "", fmt.Errorf("relay URL requires a host and no credentials or fragment")
	}
	u.Host = strings.ToLower(u.Host)
	if u.Path == "/" {
		u.Path = ""
	}
	return u.String(), nil
}

func groupName(validMembers []string) string {
	var sum uint32
	for _, m := range validMembers {
		for i := 0; i < len(m); i++ {
			sum = (sum*33 + uint32(m[i])) ^ uint32(i)
		}
	}
	return fmt.Sprintf("Group-%06x", sum&0xFFFFFF)
}

func npubToTokiPona(pubkey string) string {
	var sum [3]byte
	for i := 0; i < len(pubkey); i++ {
		sum[i%3] ^= pubkey[i]
	}
	return fmt.Sprintf("%s-%s-%s",
		tokiPonaNouns[int(sum[0])%len(tokiPonaNouns)],
		tokiPonaNouns[int(sum[1])%len(tokiPonaNouns)],
		tokiPonaNouns[int(sum[2])%len(tokiPonaNouns)],
	)
}

var tokiPonaNouns = [...]string{
	"ijo", "ilo", "insa", "jan", "jelo", "jo", "kala", "kalama", "kasi", "ken",
	"kili", "kiwen", "ko", "kon", "kulupu", "lape", "laso", "lawa", "len", "lili",
	"linja", "lipu", "loje", "luka", "lukin", "lupa", "ma", "mama", "mani", "meli",
	"mije", "moku", "moli", "monsi", "mun", "musi", "mute", "nanpa", "nasin", "nena",
	"nimi", "noka", "oko", "olin", "open", "pakala", "pali", "palisa", "pan", "pilin",
	"pipi", "poki", "pona", "selo", "sewi", "sijelo", "sike", "sitelen", "sona", "soweli",
	"suli", "suno", "supa", "suwi", "telo", "tenpo", "toki", "tomo", "unpa", "uta",
	"utala", "waso", "wawa", "weka", "wile",
}
