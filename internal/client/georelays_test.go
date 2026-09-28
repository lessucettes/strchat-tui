package client

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mmcloughlin/geohash"
)

// Tests for the geo-relay catalog:
//   - the bundled catalog is pinned (hash + entry count) and valid,
//   - CSV parsing/validation is bounded and strict,
//   - selection is deterministic and preserves the historical
//     "wss://host" output (verified against distances computed independently
//     from the upstream CSV with a Python implementation of geohash/haversine),
//   - relay selection never touches the network; an optional local cache is
//     used only while it is valid.

const geoTestCatalogHeader = "Relay URL,Latitude,Longitude\r\n"

// geoTestCatalog builds a header + CRLF catalog exactly like the upstream CSV.
func geoTestCatalog(rows ...string) string {
	return geoTestCatalogHeader + strings.Join(rows, "\r\n") + "\r\n"
}

// geoTestAppDir redirects the app config dir to a temporary XDG directory so
// cache behaviour is hermetic. Only linux honours XDG_CONFIG_HOME in
// os.UserConfigDir.
func geoTestAppDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("geo-relay cache tests redirect the config dir through XDG_CONFIG_HOME (linux only)")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return filepath.Join(dir, "strchat-tui")
}

func geoTestMustParse(t *testing.T, content string) []relayEntry {
	t.Helper()
	entries, err := parseCatalog("test catalog", []byte(content))
	if err != nil {
		t.Fatalf("parseCatalog: %v", err)
	}
	return entries
}

func TestBundledCatalogIsPinnedAndValid(t *testing.T) {
	if len(bundledCatalogCSV) == 0 {
		t.Fatal("bundled geo-relay catalog is empty")
	}
	sum := sha256.Sum256(bundledCatalogCSV)
	if got := hex.EncodeToString(sum[:]); got != bundledCatalogSHA256 {
		t.Errorf("bundled catalog sha256 = %s, want %s (update the pinned hash and count when refreshing the CSV)", got, bundledCatalogSHA256)
	}

	entries := geoTestMustParse(t, string(bundledCatalogCSV))
	if len(entries) != bundledCatalogRelayCount {
		t.Errorf("bundled catalog has %d relays, want %d", len(entries), bundledCatalogRelayCount)
	}
	hosts := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if hosts[entry.Host] {
			t.Errorf("duplicate host %q in bundled catalog", entry.Host)
		}
		hosts[entry.Host] = true
		if entry.Lat < -90 || entry.Lat > 90 || entry.Lon < -180 || entry.Lon > 180 {
			t.Errorf("entry %q has out-of-range coordinates (%v, %v)", entry.Host, entry.Lat, entry.Lon)
		}
	}
}

func TestBundledBitChatIOSCatalogIsPinnedAndValid(t *testing.T) {
	if len(bundledBitChatIOSCatalogCSV) == 0 {
		t.Fatal("bundled BitChat iOS catalog is empty")
	}
	sum := sha256.Sum256(bundledBitChatIOSCatalogCSV)
	if got := hex.EncodeToString(sum[:]); got != bundledBitChatIOSCatalogSHA256 {
		t.Errorf("bundled BitChat iOS catalog sha256 = %s, want %s (update the pinned hash and count when refreshing the CSV)",
			got, bundledBitChatIOSCatalogSHA256)
	}

	entries := geoTestMustParse(t, string(bundledBitChatIOSCatalogCSV))
	if len(entries) != bundledBitChatIOSCatalogRelayCount {
		t.Errorf("bundled BitChat iOS catalog has %d relays, want %d", len(entries), bundledBitChatIOSCatalogRelayCount)
	}
	hosts := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if hosts[entry.Host] {
			t.Errorf("duplicate host %q in bundled BitChat iOS catalog", entry.Host)
		}
		hosts[entry.Host] = true
		if entry.Lat < -90 || entry.Lat > 90 || entry.Lon < -180 || entry.Lon > 180 {
			t.Errorf("entry %q has out-of-range coordinates (%v, %v)", entry.Host, entry.Lat, entry.Lon)
		}
	}
}

// The two bundled catalogs differ: BitChat iOS ships its own reviewed list, so
// selection has to cover both nearest sets or a peer of one variant can never be
// reached. Only the upstream catalog is used here, which is the pre-merge
// behaviour.
func geoTestNearestFromCatalog(t *testing.T, data []byte, geohashStr string, count int) []string {
	t.Helper()
	entries, err := parseCatalog("test catalog", data)
	if err != nil {
		t.Fatalf("parseCatalog: %v", err)
	}
	nearest, err := selectClosestRelays(entries, geohashStr, count)
	if err != nil {
		t.Fatalf("selectClosestRelays: %v", err)
	}
	hosts := make([]string, len(nearest))
	for i, url := range nearest {
		hosts[i] = strings.TrimPrefix(url, "wss://")
	}
	return hosts
}

func TestClosestRelaysReachesBothBitChatVariants(t *testing.T) {
	geoTestAppDir(t)
	// Sample geohashes: the two catalogs' nearest-five sets overlap between 0/5
	// and 4/5 for them (computed independently from the pinned CSVs).
	for _, geohashStr := range []string{"s0000h", "dr5ru7", "u33dc0", "9q8yy"} {
		t.Run(geohashStr, func(t *testing.T) {
			iosNearest := geoTestNearestFromCatalog(t, bundledBitChatIOSCatalogCSV, geohashStr, defaultRelayCount)
			upstreamNearest := geoTestNearestFromCatalog(t, bundledCatalogCSV, geohashStr, defaultRelayCount)

			got, err := closestRelays(geohashStr, defaultRelayCount)
			if err != nil {
				t.Fatalf("closestRelays: %v", err)
			}
			selected := make(map[string]bool, len(got))
			for _, url := range got {
				selected[strings.TrimPrefix(url, "wss://")] = true
			}
			for _, host := range iosNearest {
				if !selected[host] {
					t.Errorf("selection %v misses BitChat iOS nearest relay %q", got, host)
				}
			}
			for _, host := range upstreamNearest {
				if !selected[host] {
					t.Errorf("selection %v misses upstream nearest relay %q", got, host)
				}
			}
			if len(got) > 2*defaultRelayCount {
				t.Errorf("selection returned %d relays, want at most %d", len(got), 2*defaultRelayCount)
			}
			again, err := closestRelays(geohashStr, defaultRelayCount)
			if err != nil {
				t.Fatalf("closestRelays: %v", err)
			}
			if !reflect.DeepEqual(got, again) {
				t.Errorf("closestRelays is not deterministic: %v then %v", got, again)
			}
		})
	}
}

// For "s0000h" the two nearest-five sets are disjoint, which is the worst case
// for rendezvous: an upstream-only selection shares no relay at all with a
// BitChat iOS user in that geohash.
func TestClosestRelaysWorstCaseOverlapIsDisjoint(t *testing.T) {
	geoTestAppDir(t)
	iosNearest := geoTestNearestFromCatalog(t, bundledBitChatIOSCatalogCSV, "s0000h", defaultRelayCount)
	upstreamNearest := geoTestNearestFromCatalog(t, bundledCatalogCSV, "s0000h", defaultRelayCount)
	for _, host := range iosNearest {
		for _, other := range upstreamNearest {
			if host == other {
				t.Fatalf("expected disjoint nearest sets for this sample, both contain %q", host)
			}
		}
	}
}

func TestMergeClosestPrefersSharedRelaysAndDeduplicates(t *testing.T) {
	// A relay both catalogs rank is targeted by either variant, so it must come
	// first; the remaining hosts follow in round-robin rank order.
	got := mergeClosest([][]string{{"a.example", "b.example", "c.example"}, {"b.example", "d.example"}}, 4)
	want := []string{"b.example", "a.example", "d.example", "c.example"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mergeClosest = %v, want %v", got, want)
	}
	if limited := mergeClosest([][]string{{"a.example", "b.example"}, {"c.example"}}, 2); !reflect.DeepEqual(limited, []string{"a.example", "c.example"}) {
		t.Errorf("mergeClosest with a limit = %v, want [a.example c.example], the first of each catalog before the second", limited)
	}
	if empty := mergeClosest(nil, 5); len(empty) != 0 {
		t.Errorf("mergeClosest(nil) = %v, want empty", empty)
	}
}

// The pool is what actually gets dialed, so the guarantee has to hold there:
// for a geohash, every relay in either catalog's nearest set is in it.
func TestGeohashRelayPoolCoversBothCatalogFamilies(t *testing.T) {
	geoTestAppDir(t)
	events := make(chan DisplayEvent, 8)
	c := newClient(&config{Views: []View{{Name: "s0000h"}}, ActiveViewName: "s0000h"}, nil, events)
	t.Cleanup(c.cancel)

	iosNearest := geoTestNearestFromCatalog(t, bundledBitChatIOSCatalogCSV, "s0000h", defaultRelayCount)
	upstreamNearest := geoTestNearestFromCatalog(t, bundledCatalogCSV, "s0000h", defaultRelayCount)

	pool := c.getRelayPoolForChat("s0000h")
	poolHosts := make(map[string]bool, len(pool))
	for _, url := range pool {
		poolHosts[strings.TrimPrefix(url, "wss://")] = true
	}
	for _, host := range append(append([]string{}, iosNearest...), upstreamNearest...) {
		if !poolHosts[host] {
			t.Errorf("relay pool %v does not include the nearest relay %q", pool, host)
		}
	}
	// This geohash's nearest sets are disjoint, so the pool must span both.
	iosOnly, upstreamOnly := 0, 0
	upstreamSet := make(map[string]bool, len(upstreamNearest))
	for _, host := range upstreamNearest {
		upstreamSet[host] = true
	}
	iosSet := make(map[string]bool, len(iosNearest))
	for _, host := range iosNearest {
		iosSet[host] = true
	}
	for host := range poolHosts {
		switch {
		case iosSet[host] && !upstreamSet[host]:
			iosOnly++
		case upstreamSet[host] && !iosSet[host]:
			upstreamOnly++
		}
	}
	if iosOnly == 0 || upstreamOnly == 0 {
		t.Errorf("relay pool %v covers %d BitChat iOS relays and %d upstream relays, want both families", pool, iosOnly, upstreamOnly)
	}
}

func TestParseCatalogAccepts(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    []relayEntry
	}{
		{
			name:    "upstream_format",
			content: geoTestCatalog("relay.example.com,52.52,13.405"),
			want:    []relayEntry{{Host: "relay.example.com", Lat: 52.52, Lon: 13.405}},
		},
		{
			name:    "without_header",
			content: "relay.example.com,52.52,13.405\n",
			want:    []relayEntry{{Host: "relay.example.com", Lat: 52.52, Lon: 13.405}},
		},
		{
			name:    "coordinate_bounds",
			content: geoTestCatalog("north.example.com,90,180", "south.example.com,-90,-180"),
			want: []relayEntry{
				{Host: "north.example.com", Lat: 90, Lon: 180},
				{Host: "south.example.com", Lat: -90, Lon: -180},
			},
		},
		{
			name:    "scheme_prefixes_stripped_and_port_kept",
			content: geoTestCatalog("wss://relay.example.com,1.5,2.5", "ws://relay.example.com:8443,-1.5,-2.5", "relay.example.com:443,0,0"),
			want: []relayEntry{
				{Host: "relay.example.com", Lat: 1.5, Lon: 2.5},
				{Host: "relay.example.com:8443", Lat: -1.5, Lon: -2.5},
				{Host: "relay.example.com:443", Lat: 0, Lon: 0},
			},
		},
		{
			name:    "case_and_space_normalized",
			content: geoTestCatalog("  Relay.Example.COM  , 52.5 , 13.4 "),
			want:    []relayEntry{{Host: "relay.example.com", Lat: 52.5, Lon: 13.4}},
		},
		{
			name:    "underscore_label_allowed",
			content: geoTestCatalog("relay_example.com,1,2"),
			want:    []relayEntry{{Host: "relay_example.com", Lat: 1, Lon: 2}},
		},
		{
			name:    "duplicate_host_keeps_first",
			content: geoTestCatalog("dup.example.com,1,2", "dup.example.com,9,9"),
			want:    []relayEntry{{Host: "dup.example.com", Lat: 1, Lon: 2}},
		},
		{
			name:    "extra_columns_tolerated",
			content: geoTestCatalog("relay.example.com,1,2,note"),
			want:    []relayEntry{{Host: "relay.example.com", Lat: 1, Lon: 2}},
		},
		{
			name:    "blank_lines_ignored",
			content: "Relay URL,Latitude,Longitude\n\nrelay.example.com,1,2\n\n",
			want:    []relayEntry{{Host: "relay.example.com", Lat: 1, Lon: 2}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := geoTestMustParse(t, tc.content)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseCatalog = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseCatalogRejects(t *testing.T) {
	tooMany := make([]string, 0, maxCatalogEntries+1)
	for i := range maxCatalogEntries + 1 {
		tooMany = append(tooMany, fmt.Sprintf("relay-%d.example.com,1,2", i))
	}
	cases := map[string]string{
		"empty":                    "",
		"malformed_first_data_row": "bad-relay.example.com,nope,2\nvalid.example.com,1,2\n",
		"header_only":              geoTestCatalogHeader,
		"too_few_columns":          "relay.example.com,1\n",
		"unterminated_quote":       "\"relay.example.com,1,2\n",
		"bad_latitude":             geoTestCatalog("relay.example.com,abc,2"),
		"bad_longitude":            geoTestCatalog("relay.example.com,1,x"),
		"latitude_out_of_range":    geoTestCatalog("relay.example.com,90.5,2"),
		"longitude_out_of_range":   geoTestCatalog("relay.example.com,1,-180.5"),
		"nan_latitude":             geoTestCatalog("relay.example.com,NaN,2"),
		"inf_longitude":            geoTestCatalog("relay.example.com,1,+Inf"),
		"empty_host":               geoTestCatalog(",1,2"),
		"host_with_space":          geoTestCatalog("bad host.example.com,1,2"),
		"host_with_path":           geoTestCatalog("relay.example.com/nostr,1,2"),
		"host_with_query":          geoTestCatalog("relay.example.com?a=b,1,2"),
		"host_with_leading_dot":    geoTestCatalog(".relay.example.com,1,2"),
		"host_label_dash":          geoTestCatalog("bad-.example.com,1,2"),
		"host_too_long":            geoTestCatalog(strings.Repeat("a", maxCatalogHostLen+1) + ".example.com,1,2"),
		"label_too_long":           geoTestCatalog(strings.Repeat("b", 64) + ".example.com,1,2"),
		"port_empty":               geoTestCatalog("relay.example.com:,1,2"),
		"port_not_a_number":        geoTestCatalog("relay.example.com:abc,1,2"),
		"port_zero":                geoTestCatalog("relay.example.com:0,1,2"),
		"port_too_large":           geoTestCatalog("relay.example.com:65536,1,2"),
		"too_many_entries":         geoTestCatalog(tooMany...),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseCatalog("test catalog", []byte(content)); err == nil {
				t.Fatalf("parseCatalog accepted invalid catalog:\n%s", content)
			}
		})
	}
}

// The legacy cache path belongs to the retired discovery subsystem: a file left
// there must never change rendezvous, be it valid-looking or malformed. Relay
// selection is a pure function of the bundled, pinned catalog.
func TestClosestRelaysIgnoresLegacyLocalCacheFile(t *testing.T) {
	appDir := geoTestAppDir(t)
	cachePath := filepath.Join(appDir, "georelays_cache.csv")
	if err := os.MkdirAll(appDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	baseline, err := closestRelays("s0000h", 5)
	if err != nil {
		t.Fatalf("closestRelays: %v", err)
	}

	variants := map[string]func(t *testing.T){
		"plausible_catalog": func(t *testing.T) {
			// Both relays sit at the search point, so a used cache would
			// dominate the ranking instead of the bundled result.
			writeTestFile(t, cachePath, geoTestCatalog(
				"cache-only.example.com,0.02,0.01",
				"relay02.lnfi.network,0.03,0.01",
			), 0o600)
		},
		"malformed_csv": func(t *testing.T) {
			writeTestFile(t, cachePath, geoTestCatalog("relay.example.com,abc,2"), 0o600)
		},
		"oversized": func(t *testing.T) {
			writeTestFile(t, cachePath, strings.Repeat("a", maxCatalogBytes+1), 0o600)
		},
		"directory": func(t *testing.T) {
			if err := os.Mkdir(cachePath, 0o700); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
		},
	}
	for name, setup := range variants {
		t.Run(name, func(t *testing.T) {
			if err := os.RemoveAll(cachePath); err != nil {
				t.Fatalf("remove cache: %v", err)
			}
			setup(t)

			got, err := closestRelays("s0000h", 5)
			if err != nil {
				t.Fatalf("closestRelays: %v", err)
			}
			if !reflect.DeepEqual(got, baseline) {
				t.Errorf("legacy cache file changed selection: %v, want %v", got, baseline)
			}
		})
	}
}

func TestSelectClosestRelaysOrdersDeterministically(t *testing.T) {
	const geohashStr = "s0000"
	lat, lon := geohash.DecodeCenter(geohashStr)
	entries := []relayEntry{
		{Host: "zulu.example.com", Lat: lat, Lon: lon},    // distance 0, ties with alpha
		{Host: "alpha.example.com", Lat: lat, Lon: lon},   // distance 0, ties with zulu
		{Host: "mid.example.com", Lat: lat + 1, Lon: lon}, // ~111 km
		{Host: "far.example.com", Lat: 80, Lon: -170},     // ~half the planet
	}
	want := []string{
		"wss://alpha.example.com",
		"wss://zulu.example.com",
		"wss://mid.example.com",
		"wss://far.example.com",
	}

	got, err := selectClosestRelays(entries, geohashStr, 4)
	if err != nil {
		t.Fatalf("selectClosestRelays: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selectClosestRelays = %v, want %v", got, want)
	}

	for range 5 {
		again, err := selectClosestRelays(entries, geohashStr, 4)
		if err != nil {
			t.Fatalf("selectClosestRelays: %v", err)
		}
		if !reflect.DeepEqual(again, want) {
			t.Fatalf("selectClosestRelays is not deterministic: %v", again)
		}
	}

	if clamped, err := selectClosestRelays(entries, geohashStr, 10); err != nil || len(clamped) != len(want) {
		t.Errorf("count larger than the catalog: got %v, err %v", clamped, err)
	}
	if empty, err := selectClosestRelays(entries, geohashStr, 0); err != nil || len(empty) != 0 {
		t.Errorf("count 0: got %v, err %v", empty, err)
	}
	if empty, err := selectClosestRelays(entries, geohashStr, -3); err != nil || len(empty) != 0 {
		t.Errorf("negative count: got %v, err %v", empty, err)
	}
	if empty, err := selectClosestRelays(nil, geohashStr, 5); err != nil || len(empty) != 0 {
		t.Errorf("empty catalog: got %v, err %v", empty, err)
	}
}

func TestSelectClosestRelaysRejectsInvalidGeohash(t *testing.T) {
	for _, geohashStr := range []string{"", "not a geohash", "12345678901234", "ABC", "u33dca"} {
		if _, err := selectClosestRelays(nil, geohashStr, 5); err == nil {
			t.Errorf("selectClosestRelays accepted geohash %q", geohashStr)
		}
	}
}

// Expected values were computed independently (Python implementation of
// geohash decoding and the haversine formula, R = 6371 km) from both pinned
// CSVs: for geohash "s0000h" the two catalogs' five nearest relays are disjoint
// and every candidate has a large gap to the next one, so the merged order below
// (BitChat iOS first, then upstream, alternating) is robust against tiny
// coordinate drift.
func TestClosestRelaysMergesBothPinnedCatalogs(t *testing.T) {
	appDir := geoTestAppDir(t)
	cachePath := filepath.Join(appDir, "georelays_cache.csv")

	got, err := closestRelays("s0000h", 5)
	if err != nil {
		t.Fatalf("closestRelays: %v", err)
	}
	want := []string{
		"wss://insta-relay.apps3.slidestr.net",
		"wss://21milionidinostr.duckdns.org",
		"wss://relay.nostu.be",
		"wss://openrelay.ziomc.com",
		"wss://strfry.apps3.slidestr.net",
		"wss://nostr.emanuelemiani.it",
		"wss://bitcoinostr.duckdns.org",
		"wss://aurum.saturnali.net",
		"wss://btc.klendazu.com",
		"wss://nr.yay.so",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("closestRelays(\"s0000h\", 5) = %v, want the merged nearest five of both catalogs %v", got, want)
	}
	if _, err := os.Stat(cachePath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("selection must not create a cache file (stat err: %v)", err)
	}
}

func TestClosestRelaysDoesNotUseNetwork(t *testing.T) {
	geoTestAppDir(t)
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("ALL_PROXY", "socks5://127.0.0.1:1")

	start := time.Now()
	got, err := closestRelays("s0000h", 3)
	if err != nil {
		t.Fatalf("closestRelays: %v", err)
	}
	// Three per catalog; for this geohash the two sets are disjoint.
	if len(got) != 6 {
		t.Fatalf("closestRelays returned %d relays, want 6", len(got))
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("selection took %v, which suggests network or DNS I/O", elapsed)
	}
}

func TestHaversineKnownDistances(t *testing.T) {
	cases := []struct {
		name                   string
		lat1, lon1, lat2, lon2 float64
		wantKm, toleranceKm    float64
	}{
		{"one degree at the equator", 0, 0, 0, 1, 111.195, 0.1},
		{"one degree of latitude", 0, 0, 1, 0, 111.195, 0.1},
		{"london to paris", 51.5074, -0.1278, 48.8566, 2.3522, 343.556, 1},
		{"antipodal equator", 0, 0, 0, 180, 20015.087, 5},
		{"pole to pole", 90, 0, -90, 0, 20015.087, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := haversine(tc.lat1, tc.lon1, tc.lat2, tc.lon2)
			if diff := got - tc.wantKm; diff > tc.toleranceKm || diff < -tc.toleranceKm {
				t.Errorf("haversine = %.3f km, want %.3f ± %.3f km", got, tc.wantKm, tc.toleranceKm)
			}
		})
	}
}

func BenchmarkClosestRelays(b *testing.B) {
	b.Setenv("XDG_CONFIG_HOME", b.TempDir())
	b.ReportAllocs()
	for b.Loop() {
		if _, err := closestRelays("s0000h", defaultRelayCount); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseCatalogCSV(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if _, err := parseCatalog("bundled", bundledCatalogCSV); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSelectClosestRelays(b *testing.B) {
	entries, err := parseCatalog("bundled", bundledCatalogCSV)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := selectClosestRelays(entries, "s0000h", defaultRelayCount); err != nil {
			b.Fatal(err)
		}
	}
}
