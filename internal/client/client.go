package client

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/nbd-wtf/go-nostr"
)

type client struct {
	sk                string
	pk                string
	n                 string
	config            *config
	relays            map[string]*managedRelay
	relaysMu          sync.Mutex
	seenCache         *lru.Cache[string, bool]
	seenCacheMu       sync.Mutex
	userContext       *lru.Cache[string, userContext]
	chatKeys          map[string]chatSession
	actionsChan       <-chan UserAction
	eventsChan        chan<- DisplayEvent
	filtersCompiled   []compiledPattern
	mutesCompiled     []compiledPattern
	orderMu           sync.Mutex
	orderBuf          map[string][]orderItem
	orderTimers       map[string]*time.Timer
	discoveredStore   *discoveredRelayStore
	updateSubTimer    *time.Timer
	updateSubMu       sync.Mutex
	verifyingMu       sync.Mutex
	verifying         map[string]struct{}
	activeDiscoveries int32
	verifyFailCache   *lru.Cache[string, bool]
	ctx               context.Context
	cancel            context.CancelFunc
	wg                sync.WaitGroup
}

func New(actions <-chan UserAction, events chan<- DisplayEvent) (*client, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load configuration: %w", err)
	}

	if cfg.BlockedUsers == nil {
		cfg.BlockedUsers = []blockedUser{}
	}

	seenCache, err := lru.New[string, bool](seenCacheSize)
	if err != nil {
		return nil, fmt.Errorf("failed to create seen cache: %w", err)
	}

	userContextCache, err := lru.New[string, userContext](userContextCacheSize)
	if err != nil {
		return nil, fmt.Errorf("failed to create user context cache: %w", err)
	}

	verifyFailCache, err := lru.New[string, bool](2000)
	if err != nil {
		return nil, fmt.Errorf("failed to create verify fail cache: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	client := &client{
		config:          cfg,
		actionsChan:     actions,
		eventsChan:      events,
		relays:          make(map[string]*managedRelay),
		seenCache:       seenCache,
		userContext:     userContextCache,
		chatKeys:        make(map[string]chatSession),
		orderBuf:        make(map[string][]orderItem),
		orderTimers:     make(map[string]*time.Timer),
		verifying:       make(map[string]struct{}),
		verifyFailCache: verifyFailCache,
		ctx:             ctx,
		cancel:          cancel,
	}

	if err := client.loadDiscoveredRelayStore(); err != nil {
		return nil, fmt.Errorf("failed to load relay store: %w", err)
	}

	client.rebuildRegexCaches()

	if cfg.Nick != "" {
		client.n = cfg.Nick
	}

	return client, nil
}

func (c *client) Run() {
	// ensure main keypair is loaded
	if c.sk == "" {
		if c.config.PrivateKey != "" {
			c.sk = c.config.PrivateKey
			c.pk, _ = nostr.GetPublicKey(c.sk)
		} else {
			c.sk = nostr.GeneratePrivateKey()
			c.pk, _ = nostr.GetPublicKey(c.sk)
			c.config.PrivateKey = c.sk
			c.saveConfig()
		}
	}

	identitySet := false
	if c.config.ActiveViewName != "" {
		c.setActiveView(c.config.ActiveViewName)
		identitySet = true
	} else if len(c.config.Views) > 0 {
		c.setActiveView(c.config.Views[0].Name)
		identitySet = true
	}

	if !identitySet {
		log.Println("No chat/group found on startup, generating initial ephemeral identity.")
		c.sk = nostr.GeneratePrivateKey()
		c.pk, _ = nostr.GetPublicKey(c.sk)
		if c.config.Nick != "" {
			c.n = c.config.Nick
		} else {
			c.n = npubToTokiPona(c.pk)
		}
		c.eventsChan <- DisplayEvent{
			Type:    "STATUS",
			Content: fmt.Sprintf("No chats joined. Initial identity: %s (%s...)", c.n, c.pk[:4]),
		}
	}

	c.sendStateUpdate()

	c.wg.Go(func() {
		c.updateAllSubscriptions()
		c.discoverRelays(c.config.AnchorRelays, 1)
	})

	for {
		select {
		case action, ok := <-c.actionsChan:
			if !ok {
				c.shutdown()
				return
			}
			c.handleAction(action)
		case <-c.ctx.Done():
			return
		}
	}
}

func (c *client) handleAction(action UserAction) {
	switch action.Type {
	case "SEND_MESSAGE":
		go c.publishMessage(action.Payload)
	case "ACTIVATE_VIEW":
		c.setActiveView(action.Payload)
		c.flushAllOrdering()
		c.updateAllSubscriptions()
	case "CREATE_GROUP":
		c.createGroup(action.Payload)
	case "JOIN_CHATS":
		c.joinChats(action.Payload)
	case "LEAVE_CHAT":
		c.leaveChat(action.Payload)
	case "DELETE_GROUP":
		c.deleteGroup(action.Payload)
	case "DELETE_VIEW":
		c.deleteView(action.Payload)
	case "REQUEST_NICK_COMPLETION":
		c.handleNickCompletion(action.Payload)
	case "SET_POW":
		c.setPoW(action.Payload)
	case "SET_NICK":
		c.setNick(action.Payload)
	case "LIST_CHATS":
		c.listChats()
	case "GET_ACTIVE_CHAT":
		c.getActiveChat()
	case "BLOCK_USER":
		c.blockUser(action.Payload)
	case "UNBLOCK_USER":
		c.unblockUser(action.Payload)
	case "LIST_BLOCKED":
		c.listBlockedUsers()
	case "HANDLE_FILTER":
		c.handleFilter(action.Payload)
	case "REMOVE_FILTER":
		c.removeFilter(action.Payload)
	case "CLEAR_FILTERS":
		c.clearFilters()
	case "HANDLE_MUTE":
		c.handleMute(action.Payload)
	case "REMOVE_MUTE":
		c.removeMute(action.Payload)
	case "CLEAR_MUTES":
		c.clearMutes()
	case "MANAGE_ANCHORS":
		c.manageAnchors(action.Payload)
	case "GET_HELP":
		c.getHelp()
	case "QUIT":
		c.shutdown()
	}
}

// manageAnchors handles adding/removing/listing anchor relays.
func (c *client) manageAnchors(payload string) {
	args := strings.Fields(payload)

	if len(args) == 0 {
		if len(c.config.AnchorRelays) == 0 {
			c.eventsChan <- DisplayEvent{Type: "INFO", Content: "No anchor relays set. Use /relay <url> to add one."}
			return
		}
		var builder strings.Builder
		builder.WriteString("Anchor Relays:\n")
		for i, url := range c.config.AnchorRelays {
			builder.WriteString(fmt.Sprintf("[%d] %s\n", i+1, url))
		}
		c.eventsChan <- DisplayEvent{Type: "INFO", Content: builder.String()}
		return
	}

	if len(args) == 1 {
		idx, err := strconv.Atoi(args[0])
		if err == nil {
			if idx < 1 || idx > len(c.config.AnchorRelays) {
				c.eventsChan <- DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Invalid index: %d. Use /relay to see the list.", idx)}
				return
			}
			removedURL := c.config.AnchorRelays[idx-1]
			c.config.AnchorRelays = append(c.config.AnchorRelays[:idx-1], c.config.AnchorRelays[idx:]...)
			c.saveConfig()
			c.eventsChan <- DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("Removed anchor relay: %s", removedURL)}
			go c.updateAllSubscriptions()
			return
		}
	}

	var added []string
	var invalid []string
	existingAnchors := make(map[string]struct{}, len(c.config.AnchorRelays))
	for _, anchor := range c.config.AnchorRelays {
		existingAnchors[anchor] = struct{}{}
	}

	for _, rawURL := range args {
		url, err := normalizeRelayURL(rawURL)
		if err != nil {
			invalid = append(invalid, rawURL)
			continue
		}
		if _, exists := existingAnchors[url]; exists {
			continue
		}

		c.config.AnchorRelays = append(c.config.AnchorRelays, url)
		existingAnchors[url] = struct{}{}
		added = append(added, url)
	}

	if len(invalid) > 0 {
		c.eventsChan <- DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Invalid URL(s) skipped: %s", strings.Join(invalid, ", "))}
	}

	if len(added) > 0 {
		c.saveConfig()
		c.eventsChan <- DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("Added anchor relay(s): %s", strings.Join(added, ", "))}
		go func() {
			c.updateAllSubscriptions()
			c.discoverRelays(added, 1)
		}()
	} else if len(invalid) == 0 {
		c.eventsChan <- DisplayEvent{Type: "STATUS", Content: "Specified relay(s) are already in the anchor list."}
	}
}

func (c *client) shutdown() {
	c.cancel()
	c.orderMu.Lock()
	for key, t := range c.orderTimers {
		if t.Stop() {
			go c.flushOrdered(key)
		}
	}
	c.orderTimers = make(map[string]*time.Timer)
	c.orderMu.Unlock()
	c.wg.Wait()
	select {
	case c.eventsChan <- DisplayEvent{Type: "SHUTDOWN"}:
	case <-time.After(200 * time.Millisecond):
	}
}

// Helpers

// triggerSubUpdate safely resets a timer to call updateAllSubscriptions.
func (c *client) triggerSubUpdate() {
	c.updateSubMu.Lock()
	defer c.updateSubMu.Unlock()

	if c.updateSubTimer != nil {
		c.updateSubTimer.Reset(debounceDelay)
		return
	}

	c.updateSubTimer = time.AfterFunc(debounceDelay, func() {
		c.updateAllSubscriptions()
		_ = c.saveDiscoveredRelayStore()

		c.updateSubMu.Lock()
		c.updateSubTimer = nil
		c.updateSubMu.Unlock()
	})
}

func (c *client) flushAllOrdering() {
	c.orderMu.Lock()
	keys := make([]string, 0, len(c.orderTimers))
	for k := range c.orderTimers {
		keys = append(keys, k)
	}
	c.orderMu.Unlock()
	for _, k := range keys {
		c.flushOrdered(k)
	}
}
