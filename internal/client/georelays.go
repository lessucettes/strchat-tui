package client

import (
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mmcloughlin/geohash"
)

// relayEntry holds the parsed information for a single relay from the CSV file.
type relayEntry struct {
	Host string
	Lat  float64
	Lon  float64
}

const (
	cacheFileName = "georelays_cache.csv"
	remoteURL     = "https://raw.githubusercontent.com/permissionlesstech/georelays/refs/heads/main/nostr_relays.csv"
	cacheTTL      = 24 * time.Hour
)

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

// loadRelays loads relay entries from the remote CSV, using a local cache if it's recent enough.
func loadRelays() ([]relayEntry, error) {
	appDir, err := getAppConfigDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine app config dir: %w", err)
	}
	cachePath := filepath.Join(appDir, cacheFileName)

	if info, err := os.Stat(cachePath); err == nil && time.Since(info.ModTime()) < cacheTTL {
		return parseCSV(cachePath)
	}

	resp, err := http.Get(remoteURL)
	if err != nil {
		if relays, err2 := parseCSV(cachePath); err2 == nil {
			return relays, nil
		}
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch relays: %s", resp.Status)
	}

	out, err := os.Create(cachePath)
	if err != nil {
		return nil, err
	}
	defer out.Close()
	if _, err = io.Copy(out, resp.Body); err != nil {
		return nil, err
	}

	return parseCSV(cachePath)
}

// parseCSV opens and parses the CSV file at the given path.
func parseCSV(path string) ([]relayEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	lines, err := r.ReadAll()
	if err != nil {
		return nil, err
	}

	var relays []relayEntry
	for i, line := range lines {
		if len(line) < 3 {
			continue
		}
		if i == 0 && strings.Contains(strings.ToLower(line[0]), "relay") {
			continue
		}
		lat, err1 := strconv.ParseFloat(strings.TrimSpace(line[1]), 64)
		lon, err2 := strconv.ParseFloat(strings.TrimSpace(line[2]), 64)
		if err1 != nil || err2 != nil {
			continue
		}
		host := strings.TrimSpace(line[0])
		host = strings.TrimPrefix(host, "wss://")
		host = strings.TrimPrefix(host, "ws://")
		relays = append(relays, relayEntry{Host: host, Lat: lat, Lon: lon})
	}
	return relays, nil
}

// closestRelays finds the N closest relays to a given geohash.
// It uses a locally cached CSV file of relays and their locations, refreshing it if it's older than 24 hours.
// If it fails to load or parse the relay list, it returns an error.
func closestRelays(geohashStr string, count int) ([]string, error) {
	relays, err := loadRelays()
	if err != nil {
		return nil, fmt.Errorf("could not load geo-relays: %w", err)
	}
	lat, lon := geohash.DecodeCenter(geohashStr)

	// A temporary struct to hold relays and their calculated distance for sorting.
	type relayWithDistance struct {
		url      string
		distance float64
	}

	pairs := make([]relayWithDistance, len(relays))
	for i, r := range relays {
		d := haversine(lat, lon, r.Lat, r.Lon)
		pairs[i] = relayWithDistance{url: "wss://" + r.Host, distance: d}
	}

	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].distance < pairs[j].distance
	})

	// Take the first N results.
	if count > len(pairs) {
		count = len(pairs)
	}

	result := make([]string, count)
	for i := 0; i < count; i++ {
		result[i] = pairs[i].url
	}

	return result, nil
}
