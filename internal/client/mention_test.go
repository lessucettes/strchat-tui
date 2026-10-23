package client

import (
	"fmt"
	"strings"
	"testing"
)

// TestMentionPrefixLenMatchesOriginalGrammar pins the allocation-free mention
// scanner to the exact fmt.Sprintf/HasPrefix grammar it replaced.
func TestMentionPrefixLenMatchesOriginalGrammar(t *testing.T) {
	oldMatch := func(text, nick, suffix string) (int, bool) {
		prefix := fmt.Sprintf("@%s#%s", nick, suffix)
		if !strings.HasPrefix(text, prefix) || (len(text) > len(prefix) && text[len(prefix)] != ' ') {
			return 0, false
		}
		return len(prefix), true
	}
	nicks := []string{"alice", "a b", "", "üser", "a#b", "@x", "x-y", "日本"}
	suffixes := []string{"abcd", "", "0f0f", strings.Repeat("a", 64), "a b"}
	texts := []string{
		"", "@", "@alice#abcd", "@alice#abcd hi", "@alice#abcd\t",
		"x@alice#abcd", "@alice#abcX", "@alice #abcd", "@a b#abcd hi",
		"@alice#abcd ", "@alice#abcd hi there", "@#abcd", "@#",
		"@üser#abcd", "@日本#0f0f 日本", "@a#b#abcd",
	}
	for _, nick := range nicks {
		for _, suffix := range suffixes {
			texts = append(texts, "@"+nick+"#"+suffix, "@"+nick+"#"+suffix+" rest", "@"+nick+"#"+suffix+"rest")
		}
	}
	for _, text := range texts {
		for _, nick := range nicks {
			for _, suffix := range suffixes {
				wantLen, wantOK := oldMatch(text, nick, suffix)
				gotLen, gotOK := mentionPrefixLen(text, nick, suffix)
				if gotOK != wantOK || (gotOK && gotLen != wantLen) {
					t.Fatalf("text=%q nick=%q suffix=%q: got (%d,%t) want (%d,%t)", text, nick, suffix, gotLen, gotOK, wantLen, wantOK)
				}
			}
		}
	}
}
