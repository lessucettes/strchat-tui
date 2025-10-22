package client

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

// DiscoveredRelay describes a relay entry in relays.json.
type DiscoveredRelay struct {
	URL      string `json:"url"`
	LastSeen int64  `json:"last_seen"`
}

type discoveredRelayStore struct {
	mu     sync.RWMutex
	Path   string
	Relays map[string]DiscoveredRelay
}

// --- Persistent store management ---

func (c *client) loadDiscoveredRelayStore() error {
	appConfigDir, err := getAppConfigDir()
	if err != nil {
		return err
	}
	path := filepath.Join(appConfigDir, "relays.json")

	s := &discoveredRelayStore{Path: path, Relays: make(map[string]DiscoveredRelay)}
	data, err := os.ReadFile(path)
	if err == nil {
		var tmp struct {
			Discovered []DiscoveredRelay `json:"discovered"`
		}
		if json.Unmarshal(data, &tmp) == nil {
			for _, r := range tmp.Discovered {
				s.Relays[r.URL] = r
			}
		}
	}

	c.discoveredStore = s
	return nil
}

func (c *client) saveDiscoveredRelayStore() error {
	s := c.discoveredStore
	s.mu.RLock()
	list := make([]DiscoveredRelay, 0, len(s.Relays))
	for _, r := range s.Relays {
		list = append(list, r)
	}
	s.mu.RUnlock()

	data, _ := json.MarshalIndent(map[string]any{"discovered": list}, "", "  ")
	tmpPath := s.Path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, s.Path)
}

func (c *client) getDiscoveredRelayURLs() []string {
	c.discoveredStore.mu.RLock()
	defer c.discoveredStore.mu.RUnlock()

	urls := make([]string, 0, len(c.discoveredStore.Relays))
	for url := range c.discoveredStore.Relays {
		urls = append(urls, url)
	}
	return urls
}

// --- Discovery logic ---

func (c *client) discoverRelays(anchors []string, depth int) {
	for _, anchor := range anchors {
		norm, err := normalizeRelayURL(anchor)
		if err != nil {
			continue
		}
		if c.verifyFailCache != nil && c.verifyFailCache.Contains(norm) {
			continue
		}
		c.wg.Add(1)
		go c.discoverOnAnchor(norm, depth)
	}
}

func (c *client) discoverOnAnchor(anchorURL string, depth int) {
	defer c.wg.Done()

	if depth > maxDiscoveryDepth {
		return
	}

	if atomic.LoadInt32(&c.activeDiscoveries) >= maxActiveDiscoveries {
		return
	}
	atomic.AddInt32(&c.activeDiscoveries, 1)
	defer atomic.AddInt32(&c.activeDiscoveries, -1)

	ctx, cancel := context.WithTimeout(c.ctx, connectTimeout)
	defer cancel()

	relay, err := nostr.RelayConnect(ctx, anchorURL)
	if err != nil {
		return
	}
	defer relay.Close()

	f := nostr.Filter{Kinds: []int{discoveryKind}}
	subCtx := c.ctx
	sub, err := relay.Subscribe(subCtx, nostr.Filters{f})
	if err != nil {
		return
	}
	defer sub.Unsub()

	for {
		select {
		case <-subCtx.Done():
			return
		case ev, ok := <-sub.Events:
			if !ok {
				return
			}
			c.parseRelayEvent(ev, verifyTimeout, depth)
		}
	}
}

func (c *client) parseRelayEvent(ev *nostr.Event, verifyTimeout time.Duration, depth int) {
	if ev.Kind != discoveryKind {
		return
	}

	store := c.discoveredStore
	foundNewRelay := false

	for _, tag := range ev.Tags {
		if len(tag) < 2 || tag[0] != "r" {
			continue
		}

		norm, err := normalizeRelayURL(tag[1])
		if err != nil {
			continue
		}

		if len(tag) >= 3 {
			mode := strings.ToLower(strings.TrimSpace(tag[2]))
			if mode == "read" || mode == "write" {
				continue
			}
		}

		if c.verifyFailCache != nil && c.verifyFailCache.Contains(norm) {
			continue
		}

		c.verifyingMu.Lock()

		isAnchor := false
		for _, a := range c.config.AnchorRelays {
			na, err := normalizeRelayURL(a)
			if err == nil && na == norm {
				isAnchor = true
				break
			}
		}
		if isAnchor {
			c.verifyingMu.Unlock()
			continue
		}

		if _, ok := c.relays[norm]; ok {
			c.verifyingMu.Unlock()
			continue
		}

		if _, ok := store.Relays[norm]; ok {
			c.verifyingMu.Unlock()
			continue
		}
		if _, ok := c.verifying[norm]; ok {
			c.verifyingMu.Unlock()
			continue
		}

		c.verifying[norm] = struct{}{}
		c.verifyingMu.Unlock()

		ok := c.verifyRelay(norm, verifyTimeout)

		if ok {
			store.mu.Lock()
			store.Relays[norm] = DiscoveredRelay{
				URL:      norm,
				LastSeen: time.Now().Unix(),
			}
			store.mu.Unlock()

			go c.manageRelayConnection(norm, nil)

			time.Sleep(relayAddRateLimit)
			foundNewRelay = true

			if depth < maxDiscoveryDepth {
				c.wg.Add(1)
				go c.discoverOnAnchor(norm, depth+1)
			}

		} else {
			if c.verifyFailCache != nil {
				c.verifyFailCache.Add(norm, true)
			}
		}

		c.verifyingMu.Lock()
		delete(c.verifying, norm)
		c.verifyingMu.Unlock()
	}

	if foundNewRelay {
		c.triggerSubUpdate()
	}
}

// --- Verification logic ---

func (c *client) verifyRelay(url string, timeout time.Duration) bool {
	rctx, cancel := context.WithTimeout(c.ctx, timeout)
	defer cancel()

	relay, err := nostr.RelayConnect(rctx, url)
	if err != nil {
		return false
	}
	defer relay.Close()

	dummyEvent := nostr.Event{
		CreatedAt: nostr.Now(),
		Kind:      geochatKind,
		Tags:      nostr.Tags{{"client", "strchat-tui"}},
		Content:   "",
		PubKey:    c.pk,
	}
	if c.sk == "" {
		return false
	}
	_ = dummyEvent.Sign(c.sk)

	if err := relay.Publish(rctx, dummyEvent); err != nil {
		return false
	}

	readCtx, cancelRead := context.WithTimeout(rctx, timeout/2)
	defer cancelRead()

	sub, err := relay.Subscribe(readCtx, nostr.Filters{{Kinds: []int{0}, Limit: 1}})
	if err != nil {
		return false
	}
	defer sub.Unsub()

	gotResponse := false
	for {
		select {
		case <-readCtx.Done():
			if !gotResponse {
				return false
			}
			return true

		case ev, ok := <-sub.Events:
			if !ok {
				return false
			}
			if ev != nil {
				gotResponse = true
			}

		case <-sub.EndOfStoredEvents:
			return true
		}
	}
}
