package client

import (
	"strings"
	"testing"

	"github.com/rivo/uniseg"
)

// Keep an independent reference for the Unicode and malformed UTF-8 behavior
// of the former full-boundary iterator, including nonpositive limits.
func FuzzTextTransforms(f *testing.F) {
	for _, s := range []string{"", "ordinary text", "a\r\nb	c\x1b\x7f", "Я界e\u0301", "👨‍👩‍👧‍👦🇷🇺", "\xff\xef\xbf\xbd", "ab\u0301cd", "ab\u0600cd", "\u0600abcd\u0301", "👩‍ab\u0301cd", "abcd\r\nef"} {
		f.Add(s, 3)
	}
	// Exercise the message/nick limits and the last ASCII byte joining a mark.
	for _, limit := range []int{64, MaxMsgLen} {
		prefix := strings.Repeat("a", limit-1)
		f.Add(prefix+"b\u0301", limit)
		f.Add(prefix+"b\u0301c", limit)
	}
	f.Add("a"+strings.Repeat("\u0301", 40000)+"b", 1) // Keep a very long cluster intact.
	f.Add("क्\u200dषx", 1)                            // Preserve Unicode 15.0, not the later GB9c rule.
	f.Add("a\u0301\xffb", 1)
	f.Add("a\xff\u0301b", 2)
	f.Add("ab\r\ncd", 3)
	f.Add("ordinary text", 0)
	f.Add("Я界", -1)
	f.Fuzz(func(t *testing.T, s string, limit int) {
		limit %= MaxMsgLen + 2
		var want strings.Builder
		g := uniseg.NewGraphemes(s)
		count := 0
		for g.Next() {
			if count >= limit {
				want.WriteString("...")
				break
			}
			want.WriteString(g.Str())
			count++
		}
		if got := truncateString(s, limit); got != want.String() {
			t.Fatalf("truncate(%q, %d) = %q, want %q", s, limit, got, want.String())
		}
		want.Reset()
		for _, r := range s {
			if r < 32 && r != '\n' && r != '\t' || r == 127 {
				continue
			}
			want.WriteRune(r)
		}
		if got := sanitizeString(s); got != want.String() {
			t.Fatalf("sanitize(%q) = %q, want %q", s, got, want.String())
		}
	})
}
