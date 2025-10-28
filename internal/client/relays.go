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

// Persistent store management

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

// Discovery logic

func (c *client) discoverRelays(anchors []string, depth int) {
	for _, anchor := range anchors {
		norm, err := normalizeRelayURL(anchor)
		if err != nil {
			continue
		}
		c.wg.Add(1)
		go c.discoverOnAnchor(norm, depth)
	}
}

// discoverOnAnchor connects to an anchor relay and listens for kind=10002,
// automatically reconnecting on failure. Event processing is asynchronous
// to avoid blocking the subscription feed.
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

	for {
		// If client is shutting down, exit
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		// connection with a short timeout
		connectCtx, cancelConnect := context.WithTimeout(c.ctx, connectTimeout)
		relay, err := nostr.RelayConnect(connectCtx, anchorURL)
		cancelConnect()
		if err != nil {
			time.Sleep(15 * time.Second) // wait before reconnecting
			continue
		}

		// subscription to 10002
		f := nostr.Filter{Kinds: []int{discoveryKind}}
		sub, err := relay.Subscribe(c.ctx, nostr.Filters{f})
		if err != nil {
			relay.Close()
			time.Sleep(15 * time.Second) // wait before reconnecting
			continue
		}

		// event reading loop
		for {
			select {
			case <-c.ctx.Done():
				sub.Unsub()
				relay.Close()
				return

			case ev, ok := <-sub.Events:
				if !ok {
					// connection lost - trigger reconnect
					sub.Unsub()
					relay.Close()
					time.Sleep(5 * time.Second)
					goto retry // break inner loop, continue outer
				}

				// Process async to avoid blocking the event feed
				c.wg.Add(1)
				go func(e *nostr.Event) {
					defer c.wg.Done()
					c.parseRelayEvent(e, verifyTimeout, depth)
				}(ev)
			}
		}

	retry:
		continue
	}
}

// parseRelayEvent processes a kind=10002 event and asynchronously verifies
// new relays. Verification is done in separate goroutines.
func (c *client) parseRelayEvent(ev *nostr.Event, verifyTimeout time.Duration, depth int) {
	if ev.Kind != discoveryKind {
		return
	}

	store := c.discoveredStore

	for _, tag := range ev.Tags {
		if len(tag) < 2 || tag[0] != "r" {
			continue
		}

		url, err := normalizeRelayURL(tag[1])
		if err != nil {
			continue
		}

		// skip read/write specific
		if len(tag) >= 3 {
			mode := strings.ToLower(strings.TrimSpace(tag[2]))
			if mode == "read" || mode == "write" {
				continue
			}
		}

		// skip if it's one of our own anchor relays
		isAnchor := false
		for _, a := range c.config.AnchorRelays {
			na, err := normalizeRelayURL(a)
			if err == nil && na == url {
				isAnchor = true
				break
			}
		}
		if isAnchor {
			continue
		}

		// if in fail-cache, skip
		if c.verifyFailCache != nil && c.verifyFailCache.Contains(url) {
			continue
		}

		// uniqueness check block
		c.verifyingMu.Lock()

		// already being verified
		if _, ok := c.verifying[url]; ok {
			c.verifyingMu.Unlock()
			continue
		}

		// already active
		if _, ok := c.relays[url]; ok {
			c.verifyingMu.Unlock()
			continue
		}

		// already in discovered
		if _, ok := store.Relays[url]; ok {
			c.verifyingMu.Unlock()
			continue
		}

		// mark as "being verified"
		c.verifying[url] = struct{}{}
		c.verifyingMu.Unlock()

		// async verification
		c.wg.Add(1)
		go func(url string) {
			defer c.wg.Done()

			// remove from "verifying" map when done
			defer func() {
				c.verifyingMu.Lock()
				delete(c.verifying, url)
				c.verifyingMu.Unlock()
			}()

			ok := c.verifyRelay(url, verifyTimeout)
			if !ok {
				// add to fail-cache
				if c.verifyFailCache != nil {
					c.verifyFailCache.Add(url, true)
				}
				return
			}

			// save to discoveredStore
			store.mu.Lock()
			store.Relays[url] = DiscoveredRelay{
				URL:      url,
				LastSeen: time.Now().Unix(),
			}
			store.mu.Unlock()

			// connect immediately
			go c.manageRelayConnection(url, nil)

			// update subscriptions (debounced)
			c.triggerSubUpdate()

			// recursive discovery (if depth allows)
			if depth < maxDiscoveryDepth {
				c.wg.Add(1)
				go c.discoverOnAnchor(url, depth+1)
			}

		}(url)
	}
}

// Verification logic

func (c *client) verifyRelay(url string, timeout time.Duration) bool {
	rctx, cancel := context.WithTimeout(c.ctx, timeout)
	defer cancel()

	relay, err := nostr.RelayConnect(rctx, url)
	if err != nil {
		return false
	}
	defer relay.Close()

	// create test event
	dummy := nostr.Event{
		CreatedAt: nostr.Now(),
		Kind:      geochatKind, // Kind=20000
		Tags:      nostr.Tags{{"client", "strchat-tui"}},
		Content:   "",
		PubKey:    c.pk,
	}
	if c.sk == "" {
		return false
	}
	if err := dummy.Sign(c.sk); err != nil {
		return false
	}

	// try to publish
	if err := relay.Publish(rctx, dummy); err != nil {
		return false // publish failed
	}

	// now read this event back by its ID
	readCtx, cancelRead := context.WithTimeout(rctx, timeout/2)
	defer cancelRead()

	f := nostr.Filter{
		Kinds: []int{geochatKind},
		IDs:   []string{dummy.ID},
		Limit: 1,
	}

	sub, err := relay.Subscribe(readCtx, nostr.Filters{f})
	if err != nil {
		return false
	}
	defer sub.Unsub()

	gotResponse := false
	for {
		select {
		case <-readCtx.Done():
			return gotResponse

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
