package client

import (
	"crypto/sha256"
	"fmt"
	"math/bits"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nbd-wtf/go-nostr"
)

// --- helpers ---

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

var powHintRe = regexp.MustCompile(`(?i)\b(?:attach|with)?\s*pow(?:\s+of)?\s+difficulty\s+(\d+)\b`)

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

func mrCurrentChatsLocked(sub *nostr.Subscription) []string {
	if sub == nil {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	for _, f := range sub.Filters {
		tagsToCheck := [][]string{}
		if gTags, ok := f.Tags["g"]; ok {
			tagsToCheck = append(tagsToCheck, gTags)
		}
		if dTags, ok := f.Tags["d"]; ok {
			tagsToCheck = append(tagsToCheck, dTags)
		}

		for _, tagSet := range tagsToCheck {
			for _, ch := range tagSet {
				if _, exists := seen[ch]; !exists {
					seen[ch] = struct{}{}
					out = append(out, ch)
				}
			}
		}
	}
	return out
}

func truncateString(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}

	runesCounted := 0
	for i := range s {
		runesCounted++
		if runesCounted > maxRunes {
			return s[:i] + "..."
		}
	}

	return s
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
	var prevWasRI bool
	for _, r := range s {
		if r < 32 || r == 127 {
			continue
		}
		if r < 128 {
			b.WriteRune(r)
			prevWasRI = false
			continue
		}
		if unicode.Is(unicode.M, r) {
			b.WriteRune('?')
			continue
		}
		if !unicode.IsPrint(r) {
			continue
		}
		if r >= 0x1F1E6 && r <= 0x1F1FF {
			if prevWasRI {
				b.WriteRune('?')
				prevWasRI = false
				continue
			}
			prevWasRI = true
			continue
		}
		prevWasRI = false
		b.WriteRune(r)
	}
	return b.String()
}

func npubToTokiPona(npub string) string {
	hash := sha256.Sum256([]byte(npub))
	return fmt.Sprintf("%s-%s-%s",
		tokiPonaNouns[int(hash[0])%len(tokiPonaNouns)],
		tokiPonaNouns[int(hash[1])%len(tokiPonaNouns)],
		tokiPonaNouns[int(hash[2])%len(tokiPonaNouns)],
	)
}

var tokiPonaNouns = []string{
	"ijo", "ilo", "insa", "jan", "jelo", "jo", "kala", "kalama", "kasi", "ken",
	"kili", "kiwen", "ko", "kon", "kulupu", "lape", "laso", "lawa", "len", "lili",
	"linja", "lipu", "loje", "luka", "lukin", "lupa", "ma", "mama", "mani", "meli",
	"mije", "moku", "moli", "monsi", "mun", "musi", "mute", "nanpa", "nasin", "nena",
	"nimi", "noka", "oko", "olin", "open", "pakala", "pali", "palisa", "pan", "pilin",
	"pipi", "poki", "pona", "selo", "sewi", "sijelo", "sike", "sitelen", "sona", "soweli",
	"suli", "suno", "supa", "suwi", "telo", "tenpo", "toki", "tomo", "unpa", "uta",
	"utala", "waso", "wawa", "weka", "wile",
}

func (c *client) getHelp() {
	helpText := "COMMANDS:\n" +
		"* /join <chat1> [chat2]... - Joins one or more chats. (Alias: /j)\n" +
		"* /set [name|names...] - Without args: shows active chat. With one name: activates a chat/group. With multiple names: creates a group. (Alias: /s)\n" +
		"* /list - Lists all your chats and groups. (Alias: /l)\n" +
		"* /del [name] - Deletes a chat/group. If no name, deletes the active chat/group. (Alias: /d)\n" +
		"* /nick [new_nick] - Sets or clears your nickname. (Alias: /n)\n" +
		"* /pow [number] - Sets Proof-of-Work difficulty for the active chat/group. 0 to disable. (Alias: /p)\n" +
		"* /block [@nick] - Blocks a user. Without nick, lists blocked users. (Alias: /b)\n" +
		"* /unblock [<num>|@nick|pubkey] - Unblocks a user. Without args, lists blocked users. (Alias: /ub)\n" +
		"* /filter [word|regex|<num>] - Adds a filter. Without args, lists filters. With number, toggles off/on. (Alias: /f)\n" +
		"* /unfilter [<num>] - Removes a filter by number. Without args, clears all. (Alias: /uf)\n" +
		"* /mute [word|regex|<num>] - Adds a mute. Without args, lists mutes. With number, toggles off/on. (Alias: /m)\n" +
		"* /unmute [<num>] - Removes a mute by number. Without args, clears all. (Alias: /um)\n" +
		"* /quit - Exits the application. (Alias: /q)"

	c.eventsChan <- DisplayEvent{Type: "INFO", Content: helpText}
}
