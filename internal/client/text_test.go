package client

import (
	"fmt"
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
	f.Fuzz(func(t *testing.T, s string, limit int) {
		limit %= 128
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

func BenchmarkTextTransforms(b *testing.B) {
	for _, sample := range []struct{ name, text string }{
		{"short", strings.Repeat("message ", 16)},
		{"limit", strings.Repeat("x", 2000)},
		{"long", strings.Repeat("x", 8000)},
		{"mixedLimit", performanceLongContent(b)},
		{"unicode", strings.Repeat("Я界e\u0301👨‍👩‍👧‍👦 ", 100)},
	} {
		b.Run(sample.name, func(b *testing.B) {
			b.Run("truncate", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					textTransformResult = truncateString(sample.text, MaxMsgLen)
				}
			})
			for _, dirty := range []bool{false, true} {
				b.Run(fmt.Sprintf("sanitize/dirty=%v", dirty), func(b *testing.B) {
					text := sample.text
					if dirty {
						text += "\x1b"
					}
					b.ReportAllocs()
					for b.Loop() {
						textTransformResult = sanitizeString(text)
					}
				})
			}
		})
	}
}

var textTransformResult string
