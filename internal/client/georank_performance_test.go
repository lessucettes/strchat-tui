package client

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"sort"
	"testing"

	"github.com/mmcloughlin/geohash"
)

func TestNearestSelectionMatchesFullRanking(t *testing.T) {
	catalogs, err := bundledCatalogEntries()
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewPCG(37, 59))
	for _, catalog := range catalogs {
		for round := range 30 {
			entries := slices.Clone(catalog)
			rng.Shuffle(len(entries), func(i, j int) { entries[i], entries[j] = entries[j], entries[i] })
			hash := geohash.EncodeWithPrecision(rng.Float64()*180-90, rng.Float64()*360-180, 6)
			lat, lon := geohash.DecodeCenter(hash)
			// Duplicate coordinates force the host-name tie-break at the cutoff.
			entries = append(entries, relayEntry{Host: "a.example", Lat: lat, Lon: lon}, relayEntry{Host: "z.example", Lat: lat, Lon: lon})
			ordered := slices.Clone(entries)
			sort.Slice(ordered, func(i, j int) bool {
				a, b := ordered[i], ordered[j]
				da, db := haversine(lat, lon, a.Lat, a.Lon), haversine(lat, lon, b.Lat, b.Lon)
				if da != db {
					return da < db
				}
				return a.Host < b.Host
			})
			for _, count := range []int{1, 5, 10, 64, len(entries), len(entries) + 1} {
				got, err := selectClosestHosts(entries, hash, count)
				if err != nil {
					t.Fatal(err)
				}
				want := make([]string, min(count, len(entries)))
				for i := range want {
					want[i] = ordered[i].Host
				}
				if !slices.Equal(got, want) {
					t.Fatalf("round %d count %d: got %v, want %v", round, count, got, want)
				}
			}
		}
	}
}

func BenchmarkNearestSelection(b *testing.B) {
	catalogs, err := bundledCatalogEntries()
	if err != nil {
		b.Fatal(err)
	}
	entries := catalogs[0]
	for _, count := range []int{5, 10, 64, len(entries)} {
		b.Run(fmt.Sprintf("count%d", count), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := selectClosestHosts(entries, "s0000h", count); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
