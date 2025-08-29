// package client encapsulates all logic for interacting with the Nostr protocol,
// including managing relay connections, handling subscriptions, and publishing events.
package client

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/mmcloughlin/geohash"
	"github.com/nbd-wtf/go-nostr"
)

// UserAction represents an action initiated by the user in the TUI.
type UserAction struct {
	Type    string
	Payload string
}

// DisplayEvent represents a piece of information that should be rendered by the TUI.
type DisplayEvent struct {
	Type      string
	Timestamp string
	Nick      string
	Color     string
	Content   string
	PubKey    string
	RelayURL  string
	ID        string
	Chat      string
}

// ManagedRelay wraps a standard nostr.Relay object to include
// custom metadata, such as connection latency.
type ManagedRelay struct {
	Relay   *nostr.Relay
	Latency time.Duration
}

// Client is the main struct that manages the state of the Nostr client.
type Client struct {
	sk string // private key
	pk string // public key
	n  string // toki pona nickname

	// relays holds the pool of currently active relay connections.
	relays   []*ManagedRelay
	relaysMu sync.Mutex // relaysMu protects the relays slice from concurrent access.

	seenCache   *lru.Cache[string, bool] // Thread-safe LRU cache.
	seenCacheMu sync.Mutex               // Protects the check-then-add logic for seenCache.

	// failures tracks the number of consecutive failures for each relay URL.
	failures   map[string]int
	failuresMu sync.Mutex // failuresMu protects the failures map.

	// subCtx and cancelSub manage the lifecycle of the current subscription.
	subCtx    context.Context
	cancelSub context.CancelFunc

	// Channels for communicating with the TUI.
	actionsChan <-chan UserAction
	eventsChan  chan<- DisplayEvent

	// Current chat state.
	chat     string
	chatKind int
	chatTag  string
}

// New creates and initializes a new Client instance.
func New(actions <-chan UserAction, events chan<- DisplayEvent) (*Client, error) {
	sk := nostr.GeneratePrivateKey()
	pk, err := nostr.GetPublicKey(sk)
	if err != nil {
		return nil, fmt.Errorf("failed to get public key: %w", err)
	}
	n := npubToTokiPona(pk)

	// LRU-cache
	const maxCacheSize = 50000
	cache, err := lru.New[string, bool](maxCacheSize)
	if err != nil {
		return nil, fmt.Errorf("failed to create LRU cache: %w", err)
	}

	return &Client{
		sk:          sk,
		pk:          pk,
		n:           n,
		chat:        "21m", // Default chat
		actionsChan: actions,
		eventsChan:  events,
		seenCache:   cache,
		failures:    make(map[string]int),
	}, nil
}

// Run starts the client's main loop, listening for and processing actions from the TUI.
func (c *Client) Run() {
	c.updateSubscription() // Initial subscription

	for action := range c.actionsChan {
		switch action.Type {
		case "SEND_MESSAGE":
			c.publishMessage(action.Payload)
		case "SWITCH_CHAT":
			c.chat = action.Payload
			c.updateSubscription()
		case "QUIT":
			if c.cancelSub != nil {
				c.cancelSub()
			}
			close(c.eventsChan)
			return
		}
	}
}

// updateSubscription handles the logic of switching to a new chat room.
// It cancels the previous subscription, determines the new chat parameters,
// finds the appropriate relays, and launches new manageRelay goroutines.
func (c *Client) updateSubscription() {
	if c.cancelSub != nil {
		// This is not the first run, so we are switching chats.
		c.eventsChan <- DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("Switching chat to '%s'. Disconnecting...", c.chat)}
		c.cancelSub()
	} else {
		// This is the very first run.
		c.eventsChan <- DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("Connecting to chat '%s'...", c.chat)}
	}

	c.relaysMu.Lock()
	c.relays = nil
	c.relaysMu.Unlock()

	c.subCtx, c.cancelSub = context.WithCancel(context.Background())

	var relayURLs []string
	var err error

	// Check if the chat name is a valid geohash.
	// If so, use geo-chat kinds and find the closest relays.
	// Otherwise, treat it as a standard global chat.
	if err = geohash.Validate(c.chat); err != nil {
		c.chatKind = 23333
		c.chatTag = "d"
		relayURLs = []string{"wss://relay.primal.net", "wss://relay.damus.io", "wss://offchain.pub", "wss://nos.lol", "wss://adre.su"}
	} else {
		c.chatKind = 20000
		c.chatTag = "g"
		relayURLs, err = ClosestRelays(c.chat, 5)
		if err != nil || len(relayURLs) == 0 {
			// If geo-relays fail, fall back to a default list to ensure connectivity.
			c.eventsChan <- DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Failed to get geo-relays: %v. Using defaults.", err)}
			relayURLs = []string{"wss://relay.damus.io", "wss://relay.primal.net", "wss://nos.lol"}
		}
	}

	for _, url := range relayURLs {
		go c.manageRelay(c.subCtx, url)
	}
}

// publishMessage creates, signs, and publishes a new event to the fastest connected relays.
func (c *Client) publishMessage(message string) {
	ev := nostr.Event{
		Kind:      c.chatKind,
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Tags:      nostr.Tags{{c.chatTag, c.chat}, {"n", c.n}},
		Content:   message,
		PubKey:    c.pk,
	}

	_ = ev.Sign(c.sk)

	pubCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c.relaysMu.Lock()
	currentRelays := make([]*ManagedRelay, len(c.relays))
	copy(currentRelays, c.relays)
	c.relaysMu.Unlock()

	// Sort relays by latency to publish to the fastest ones first.
	sort.Slice(currentRelays, func(i, j int) bool {
		return currentRelays[i].Latency < currentRelays[j].Latency
	})

	numToPublish := 5
	if len(currentRelays) < numToPublish {
		numToPublish = len(currentRelays)
	}

	fastestRelays := currentRelays[:numToPublish]

	if len(fastestRelays) == 0 {
		c.eventsChan <- DisplayEvent{Type: "ERROR", Content: "No connected relays to publish to"}
		return
	}

	success := false
	for _, managed := range fastestRelays {
		if err := managed.Relay.Publish(pubCtx, ev); err != nil {
			c.eventsChan <- DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Failed to publish to %s: %v", managed.Relay.URL, err)}
			continue
		}
		statusMsg := fmt.Sprintf("Published event (id: %s) to %s", ev.ID[:4], managed.Relay.URL)
		c.eventsChan <- DisplayEvent{Type: "STATUS", Content: statusMsg}
		success = true
	}

	if !success {
		c.eventsChan <- DisplayEvent{Type: "ERROR", Content: "Failed to send message to any of the fastest relays"}
	}
}

// manageRelay is a long-running goroutine that manages the connection and subscription
// to a single relay for the duration of the parent context.
func (c *Client) manageRelay(ctx context.Context, url string) {
	const maxRetries = 3
	const retryDelay = 5 * time.Second
	const connectTimeout = 30 * time.Second
	const failureThreshold = 3

	for {
		select {
		case <-ctx.Done():
			// The parent context was cancelled (e.g., switching chat), so this goroutine should exit.
			return
		default:
			connectCtx, connectCancel := context.WithTimeout(ctx, connectTimeout)
			r, latency, err := tryConnect(connectCtx, url, maxRetries)
			connectCancel()

			if err != nil {
				c.eventsChan <- DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Failed to connect to %s: %v", url, err)}
				time.Sleep(retryDelay)
				continue
			}

			// On a successful (re)connection, reset the failure counter for this relay.
			c.failuresMu.Lock()
			c.failures[url] = 0
			c.failuresMu.Unlock()

			managed := &ManagedRelay{
				Relay:   r,
				Latency: latency,
			}
			statusMsg := fmt.Sprintf("Connected to relay: %s (%dms)", url, latency.Milliseconds())
			c.eventsChan <- DisplayEvent{Type: "STATUS", Content: statusMsg}

			c.relaysMu.Lock()
			c.relays = append(c.relays, managed)
			c.relaysMu.Unlock()

			subscriptionTime := nostr.Now()

			filter := nostr.Filter{
				Kinds: []int{c.chatKind},
				Tags:  nostr.TagMap{c.chatTag: []string{c.chat}},
				Since: &subscriptionTime,
			}

			sub, err := r.Subscribe(ctx, []nostr.Filter{filter})
			if err != nil {
				c.eventsChan <- DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Error subscribing to %s: %v", url, err)}
				r.Close()
				c.relaysMu.Lock()
				c.relays = removeRelay(c.relays, r)
				c.relaysMu.Unlock()
				time.Sleep(retryDelay)
				continue
			}

			// Inner loop for processing events from the subscription.
			for {
				select {
				case <-ctx.Done():
					// The parent context was cancelled. Clean up and exit.
					r.Close()
					c.relaysMu.Lock()
					c.relays = removeRelay(c.relays, r)
					c.relaysMu.Unlock()
					return
				case ev, ok := <-sub.Events:
					if !ok {
						// The event channel was closed by the library, likely due to a
						// connection drop or a problematic event (poison pill).
						c.failuresMu.Lock()
						c.failures[url]++
						failureCount := c.failures[url]
						c.failuresMu.Unlock()

						// Implement a circuit breaker: if the relay fails too many times, abandon it.
						if failureCount >= failureThreshold {
							c.eventsChan <- DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Relay %s is unstable, abandoning connection", url)}
							r.Close()
							c.relaysMu.Lock()
							c.relays = removeRelay(c.relays, r)
							c.relaysMu.Unlock()
							return // Exit the goroutine permanently.
						}

						c.eventsChan <- DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("Event channel closed for %s, reconnecting (attempt %d/%d)", url, failureCount, failureThreshold)}
						r.Close()
						c.relaysMu.Lock()
						c.relays = removeRelay(c.relays, r)
						c.relaysMu.Unlock()
						time.Sleep(retryDelay)
						break // Break from the inner loop to trigger reconnection in the outer loop.
					}

					c.seenCacheMu.Lock()
					if c.seenCache.Contains(ev.ID) {
						c.seenCacheMu.Unlock()
						continue
					}
					c.seenCache.Add(ev.ID, true)
					c.seenCacheMu.Unlock()

					// Extract nickname or generate one.
					nValue := ev.Tags.Find("n")
					nick := ""
					if len(nValue) > 1 {
						nick = nValue[1]
					} else {
						nick = npubToTokiPona(ev.PubKey)
					}

					timestamp := time.Unix(int64(ev.CreatedAt), 0).Format("15:04:05")
					color := pubkeyToColor(ev.PubKey)
					c.eventsChan <- DisplayEvent{
						Type:      "NEW_MESSAGE",
						Timestamp: timestamp,
						Nick:      nick,
						Color:     color,
						PubKey:    ev.PubKey[:4],
						Content:   ev.Content,
						ID:        ev.ID[:4],
						Chat:      ev.Tags.Find(c.chatTag)[1],
						RelayURL:  url,
					}
				}
			}
		}
	}
}

// removeRelay safely removes a relay from the slice of managed relays.
func removeRelay(relays []*ManagedRelay, r *nostr.Relay) []*ManagedRelay {
	for i, managedRelay := range relays {
		if managedRelay.Relay == r {
			return append(relays[:i], relays[i+1:]...)
		}
	}
	return relays
}

// tryConnect attempts to connect to a relay URL with a specified number of retries and exponential backoff.
// It returns the relay connection, the connection latency, and an error if all retries fail.
func tryConnect(ctx context.Context, url string, retries int) (*nostr.Relay, time.Duration, error) {
	for i := 0; i < retries; i++ {
		start := time.Now()
		r, err := nostr.RelayConnect(ctx, url)
		latency := time.Since(start)

		if err == nil {
			return r, latency, nil
		}
		// Wait before the next retry, but also listen for context cancellation.
		select {
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		case <-time.After(time.Second * time.Duration(i+1)):
		}
	}
	return nil, 0, fmt.Errorf("failed to connect after %d retries", retries)
}

// pubkeyToColor generates a consistent color from a predefined palette based on the user's public key.
func pubkeyToColor(pubkey string) string {
	hackerPalette := []string{
		"[#00ff00]", // Lime
		"[#33ccff]", // Cyan
		"[#ff00ff]", // Magenta
		"[#ffff00]", // Yellow
		"[#ff6600]", // Amber
		"[#5fafd7]", // Light Blue
	}

	// Use a hash of the pubkey to deterministically pick a color from the palette.
	hash := sha256.Sum256([]byte(pubkey))
	colorIndex := int(hash[0]) % len(hackerPalette)

	return hackerPalette[colorIndex]
}

// npubToTokiPona generates a memorable, three-word name from a public key using Toki Pona nouns.
func npubToTokiPona(npub string) string {
	hash := sha256.Sum256([]byte(npub))

	// Take the first three bytes of the hash as indices.
	idx1 := int(hash[0]) % len(tokiPonaNouns)
	idx2 := int(hash[1]) % len(tokiPonaNouns)
	idx3 := int(hash[2]) % len(tokiPonaNouns)

	return fmt.Sprintf(
		"%s-%s-%s",
		tokiPonaNouns[idx1],
		tokiPonaNouns[idx2],
		tokiPonaNouns[idx3],
	)
}

// tokiPonaNouns is a list of nouns from the Toki Pona language, used for name generation.
var tokiPonaNouns = []string{
	"ijo", "ilo", "insa", "jan", "jelo", "jo", "kala", "kalama", "kasi", "ken",
	"kili", "kiwen", "ko", "kon", "kulupu", "lape", "laso", "lawa", "len", "lili",
	"linja", "lipu", "loje", "luka", "lukin", "lupa", "ma", "mama", "mani", "meli",
	"mije", "moku", "moli", "monsi", "mun", "musi", "mute", "nanpa", "nasin", "nena",
	"nimi", "noka", "oko", "olin", "open", "pakala", "pali", "palisa", "pan",
	"pilin", "pipi", "poki", "pona", "selo", "sewi", "sijelo", "sike", "sitelen", "sona",
	"soweli", "suli", "suno", "supa", "suwi", "telo", "tenpo", "toki", "tomo", "unpa",
	"uta", "utala", "waso", "wawa", "weka", "wile",
}
