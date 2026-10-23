package client

import (
	"strings"
	"testing"

	"github.com/mmcloughlin/geohash"
)

func FuzzGeohashNameClassification(f *testing.F) {
	for _, name := range []string{"", "lobby", "ucfv0", "COFFEE", "123456789012", "1234567890123", "界", "\xff"} {
		f.Add(name)
	}
	for i := range 256 {
		f.Add(string([]byte{byte(i)}))
	}
	f.Fuzz(func(t *testing.T, name string) {
		if got, want := isGeohashName(name), geohash.Validate(name) == nil; got != want {
			t.Fatalf("classify %q: %v, want %v", name, got, want)
		}
	})
}

func BenchmarkGeohashClassification(b *testing.B) {
	for _, name := range []string{"lobby", "ucfv0", strings.Repeat("z", 12)} {
		b.Run(name, func(b *testing.B) {
			b.Run("error", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					classificationResult = geohash.Validate(name) == nil
				}
			})
			b.Run("predicate", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					classificationResult = isGeohashName(name)
				}
			})
		})
	}
}

var classificationResult bool
