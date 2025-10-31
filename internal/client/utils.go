package client

import (
	"fmt"
	"math/bits"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"

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

var powHintRe = regexp.MustCompile(`(?i)pow[^0-9]{0,10}(\d+)`)

func parsePowHint(s string) (int, bool) {
	m := powHintRe.FindStringSubmatch(s)
	if len(m) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func safeSuffix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[string]struct{}, len(a))
	for _, s := range a {
		m[s] = struct{}{}
	}
	for _, s := range b {
		if _, ok := m[s]; !ok {
			return false
		}
	}
	return true
}

func truncateString(s string, maxClusters int) string {
	g := uniseg.NewGraphemes(s)
	var b strings.Builder
	count := 0
	for g.Next() {
		if count >= maxClusters {
			b.WriteString("...")
			break
		}
		b.WriteString(g.Str())
		count++
	}
	return b.String()
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
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 32 && r != '\n' && r != '\t' {
			continue
		}
		if r == 127 {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func normalizeRelayURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimRight(raw, "/,.;")

	if !strings.Contains(raw, "://") {
		raw = "wss://" + raw
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "wss" {
		return "", fmt.Errorf("only wss:// relays are allowed (got %s)", scheme)
	}

	host := strings.ToLower(strings.Trim(u.Host, "/."))

	if host == "" {
		if u.Path != "" && !strings.Contains(u.Path, "/") {
			host = strings.ToLower(strings.Trim(u.Path, "/."))
		}
		if host == "" {
			return "", fmt.Errorf("missing host in URL: %q", raw)
		}
	}

	return fmt.Sprintf("wss://%s", host), nil
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
