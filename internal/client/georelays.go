package client

import (
	"bytes"
	_ "embed"
	"encoding/csv"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/mmcloughlin/geohash"
)

// Geo-relay selection.
//
// closestRelays picks the relays nearest to a geohash from the catalogs bundled
// into the binary. It performs no network or disk I/O, so callers such as
// getRelayPoolForChat stay synchronous and work offline.
//
// Two catalogs are bundled because the BitChat variants do not agree on one:
// iOS ships its own reviewed list (441 entries), while the Android app and this
// client use the upstream georelays list (374 entries). The two "closest five"
// sets overlap by 0/5 to 4/5 depending on the geohash, so selecting from a
// single catalog can share no relay at all with a peer of the other variant.
// closestRelays therefore takes the nearest relays of each catalog and merges
// them, which is the whole point of bundling both.
//
// Both CSVs are verbatim copies, pinned by sha256 and entry count so selection
// is reproducible; see the constants below for the upstream revisions. There is
// no runtime refresh: a catalog changes only by upgrading this binary.

// relayEntry holds the parsed information for a single relay from the catalog.
type relayEntry struct {
	Host string
	Lat  float64
	Lon  float64
}

const (
	// bundledCatalogSHA256 and bundledCatalogRelayCount pin the embedded
	// georelays_catalog.csv (14,251 bytes, fetched 2026-09-28) to an exact
	// upstream revision. Update both when refreshing the CSV.
	bundledCatalogSHA256     = "028affec5575471672ad3f75589cccfc515b67c7af5410a76667694ef03489ee"
	bundledCatalogRelayCount = 374

	// georelaysCatalogRevision is the upstream commit the georelays CSV came
	// from: https://github.com/permissionlesstech/georelays (nostr_relays.csv).
	georelaysCatalogRevision = "6f86599694730ee2eefacfc16182641e4253a026"

	// bundledBitChatIOSCatalogSHA256 and bundledBitChatIOSCatalogRelayCount pin
	// the embedded georelays_bitchat_ios.csv (17,075 bytes, fetched 2026-09-28),
	// a verbatim copy of the relay list the BitChat iOS app ships and targets.
	// Update both when refreshing the CSV.
	bundledBitChatIOSCatalogSHA256     = "811523100064820b024714eaf3bbe0a9ba99a3c257e453e01bc1bc0f3bf8401a"
	bundledBitChatIOSCatalogRelayCount = 441

	// bitChatIOSCatalogRevision is the pinned BitChat commit the iOS CSV came
	// from: relays/online_relays_gps.csv under
	// https://github.com/permissionlesstech/bitchat.
	bitChatIOSCatalogRevision = "5e9287fae1e5fea80ca741d4ea669829dc16f144"

	// maxCatalogBytes caps every catalog read, from disk or network.
	maxCatalogBytes = 1 << 20 // 1 MiB
	// maxCatalogEntries caps the number of relays accepted from any catalog.
	maxCatalogEntries = 4096
	// maxCatalogHostLen caps a relay host including an optional port.
	maxCatalogHostLen = 300
)

//go:embed georelays_catalog.csv
var bundledCatalogCSV []byte

//go:embed georelays_bitchat_ios.csv
var bundledBitChatIOSCatalogCSV []byte

// bundledCatalog is one relay catalog compiled into the binary.
type bundledCatalog struct {
	// name labels the catalog in error messages.
	name string
	// revision is the upstream commit the CSV was taken from.
	revision string
	// sha256 and count pin the exact bytes (checked by tests).
	sha256 string
	count  int
	data   []byte
}

// bundledCatalogs lists the catalogs closestRelays selects from. Order matters
// only for the alternation of the merged result: the first catalog contributes
// the first relay when both have equally ranked candidates.
var bundledCatalogs = []bundledCatalog{
	{
		name:     "BitChat iOS catalog",
		revision: bitChatIOSCatalogRevision,
		sha256:   bundledBitChatIOSCatalogSHA256,
		count:    bundledBitChatIOSCatalogRelayCount,
		data:     bundledBitChatIOSCatalogCSV,
	},
	{
		name:     "georelays catalog",
		revision: georelaysCatalogRevision,
		sha256:   bundledCatalogSHA256,
		count:    bundledCatalogRelayCount,
		data:     bundledCatalogCSV,
	},
}

// bundledCatalogEntries parses the bundled catalogs once, in the order of
// bundledCatalogs. The embedded bytes cannot change while the process runs, and
// parsing both catalogs on every selection would cost about a millisecond and
// two thousand allocations per call for no benefit.
var bundledCatalogEntries = sync.OnceValues(func() ([][]relayEntry, error) {
	parsed := make([][]relayEntry, 0, len(bundledCatalogs))
	for _, catalog := range bundledCatalogs {
		entries, err := parseCatalog(catalog.name, catalog.data)
		if err != nil {
			return nil, fmt.Errorf("could not load geo-relays: %w", err)
		}
		parsed = append(parsed, entries)
	}
	return parsed, nil
})

// haversine calculates the great-circle distance in kilometers between two points on the Earth.
func haversine(lat1, lon1, lat2, lon2 float64) float64 {
	const (
		radius = 6371.0 // Earth radius in kilometers
		deg    = math.Pi / 180
	)
	dLat := (lat2 - lat1) * deg
	dLon := (lon2 - lon1) * deg
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*deg)*math.Cos(lat2*deg)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * radius * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// closestRelays returns up to perCatalog relays nearest to the center of the
// given geohash from each bundled catalog, merged and deduplicated: relays that
// both catalogs rank first (either variant targets them), then the remaining
// candidates alternating between catalogs so neither variant loses its nearest
// relays. The result therefore holds at most 2*perCatalog relays, which
// getRelayPoolForChat trims together with configured relays. Selection uses the
// bundled catalogs only and is safe to call from the UI/client loop.
//
// A catalog that fails to parse fails the whole selection instead of silently
// shrinking the relay set; both bundled files are pinned by tests, so this is a
// build-time problem rather than a runtime one.
func closestRelays(geohashStr string, perCatalog int) ([]string, error) {
	if err := validateGeohash(geohashStr); err != nil {
		return nil, err
	}
	if perCatalog <= 0 {
		return []string{}, nil
	}

	ranked := make([][]string, 0, len(bundledCatalogs))
	allEntries, err := bundledCatalogEntries()
	if err != nil {
		return nil, err
	}
	for _, entries := range allEntries {
		nearest, err := selectClosestHosts(entries, geohashStr, perCatalog)
		if err != nil {
			return nil, err
		}
		ranked = append(ranked, nearest)
	}

	hosts := mergeClosest(ranked, 2*perCatalog)
	urls := make([]string, len(hosts))
	for i, host := range hosts {
		urls[i] = "wss://" + host
	}
	return urls, nil
}

// mergeClosest merges per-catalog ranked host lists into one preference order.
// Hosts that more than one catalog ranks come first, because a peer of any of
// those variants targets them; the rest follow in rank order, alternating
// between catalogs, so no variant loses its nearest relays. Duplicates appear
// once, and the result is capped at limit entries.
func mergeClosest(lists [][]string, limit int) []string {
	if limit <= 0 {
		return []string{}
	}
	rankedBy := make(map[string]int)
	for _, list := range lists {
		for _, host := range list {
			rankedBy[host]++
		}
	}
	longest := 0
	for _, list := range lists {
		if len(list) > longest {
			longest = len(list)
		}
	}

	merged := make([]string, 0, limit)
	seen := make(map[string]bool)
	appendHost := func(host string) {
		if !seen[host] && len(merged) < limit {
			seen[host] = true
			merged = append(merged, host)
		}
	}
	// Pass 1: relays ranked by more than one catalog.
	for rank := 0; rank < longest; rank++ {
		for _, list := range lists {
			if rank < len(list) && rankedBy[list[rank]] > 1 {
				appendHost(list[rank])
			}
		}
	}
	// Pass 2: everything else, alternating between catalogs by rank.
	for rank := 0; rank < longest; rank++ {
		for _, list := range lists {
			if rank < len(list) {
				appendHost(list[rank])
			}
		}
	}
	return merged
}

// validateGeohash rejects geohashes that cannot be decoded.
func validateGeohash(geohashStr string) error {
	if geohashStr == "" {
		return errors.New("empty geohash")
	}
	if err := geohash.Validate(geohashStr); err != nil {
		return fmt.Errorf("invalid geohash %q: %w", geohashStr, err)
	}
	return nil
}

// selectClosestRelays returns up to count "wss://host" URLs, nearest first.
// Ordering is deterministic: ascending haversine distance, ties broken by
// host name.
func selectClosestRelays(entries []relayEntry, geohashStr string, count int) ([]string, error) {
	hosts, err := selectClosestHosts(entries, geohashStr, count)
	if err != nil {
		return nil, err
	}
	urls := make([]string, len(hosts))
	for i, host := range hosts {
		urls[i] = "wss://" + host
	}
	return urls, nil
}

// selectClosestHosts returns up to count catalog hosts, nearest first.
func selectClosestHosts(entries []relayEntry, geohashStr string, count int) ([]string, error) {
	if err := validateGeohash(geohashStr); err != nil {
		return nil, err
	}
	if count <= 0 || len(entries) == 0 {
		return []string{}, nil
	}
	if count > len(entries) {
		count = len(entries)
	}

	lat, lon := geohash.DecodeCenter(geohashStr)

	type relayDistance struct {
		host     string
		distance float64
	}
	ranked := make([]relayDistance, len(entries))
	for i, entry := range entries {
		ranked[i] = relayDistance{host: entry.Host, distance: haversine(lat, lon, entry.Lat, entry.Lon)}
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].distance != ranked[j].distance {
			return ranked[i].distance < ranked[j].distance
		}
		return ranked[i].host < ranked[j].host
	})

	hosts := make([]string, count)
	for i := range hosts {
		hosts[i] = ranked[i].host
	}
	return hosts, nil
}

// parseCatalog parses a catalog in the upstream CSV format
// ("Relay URL,Latitude,Longitude", optionally without the header). Rows are
// validated strictly: a malformed row rejects the whole catalog instead of
// silently shrinking the relay set.
func parseCatalog(name string, data []byte) ([]relayEntry, error) {
	reader := csv.NewReader(bytes.NewReader(data))
	reader.FieldsPerRecord = -1 // validated here, not by the reader
	reader.TrimLeadingSpace = true

	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("%s: malformed CSV: %w", name, err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("%s: empty catalog", name)
	}

	entries := make([]relayEntry, 0, len(records))
	seen := make(map[string]struct{}, len(records))
	for i, record := range records {
		if i == 0 && catalogHeaderRow(record) {
			continue
		}
		if len(record) < 3 {
			return nil, fmt.Errorf("%s: row %d: expected at least 3 columns, got %d", name, i+1, len(record))
		}
		if len(entries) >= maxCatalogEntries {
			return nil, fmt.Errorf("%s: more than %d relay entries", name, maxCatalogEntries)
		}
		host, err := normalizeCatalogHost(record[0])
		if err != nil {
			return nil, fmt.Errorf("%s: row %d: %w", name, i+1, err)
		}
		lat, err := parseCatalogCoordinate(record[1], "latitude", -90, 90)
		if err != nil {
			return nil, fmt.Errorf("%s: row %d: %w", name, i+1, err)
		}
		lon, err := parseCatalogCoordinate(record[2], "longitude", -180, 180)
		if err != nil {
			return nil, fmt.Errorf("%s: row %d: %w", name, i+1, err)
		}
		if _, duplicate := seen[host]; duplicate {
			continue // keep the first occurrence
		}
		seen[host] = struct{}{}
		entries = append(entries, relayEntry{Host: host, Lat: lat, Lon: lon})
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("%s: no relay entries", name)
	}
	return entries, nil
}

func catalogHeaderRow(record []string) bool {
	return len(record) >= 3 && strings.EqualFold(strings.TrimSpace(record[0]), "Relay URL") &&
		strings.EqualFold(strings.TrimSpace(record[1]), "Latitude") && strings.EqualFold(strings.TrimSpace(record[2]), "Longitude")
}

// normalizeCatalogHost validates and canonicalizes a host from the catalog and
// returns it lower-cased, with an optional numeric port preserved. The
// historical parsers also accepted entries that repeated the wss:// or ws://
// scheme, so those prefixes are stripped for compatibility.
func normalizeCatalogHost(raw string) (string, error) {
	host := strings.ToLower(strings.TrimSpace(raw))
	host = strings.TrimPrefix(host, "wss://")
	host = strings.TrimPrefix(host, "ws://")
	if host == "" {
		return "", errors.New("empty relay host")
	}
	if len(host) > maxCatalogHostLen {
		return "", fmt.Errorf("relay host exceeds %d characters", maxCatalogHostLen)
	}

	name, port, hasPort := strings.Cut(host, ":")
	if hasPort {
		if port == "" {
			return "", errors.New("relay host has an empty port")
		}
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return "", fmt.Errorf("invalid relay port %q", port)
		}
	}
	if name == "" {
		return "", errors.New("empty relay host")
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' {
			continue
		}
		return "", fmt.Errorf("invalid character %q in relay host %q", c, name)
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" {
			return "", fmt.Errorf("empty label in relay host %q", name)
		}
		if len(label) > 63 {
			return "", fmt.Errorf("relay host label exceeds 63 characters")
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("relay host label %q starts or ends with a dash", label)
		}
	}
	return host, nil
}

// parseCatalogCoordinate parses a coordinate that must be finite and inside
// the valid lat/lon range.
func parseCatalogCoordinate(raw, label string, min, max float64) (float64, error) {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q", label, strings.TrimSpace(raw))
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value < min || value > max {
		return 0, fmt.Errorf("%s %v out of range [%v, %v]", label, value, min, max)
	}
	return value, nil
}
