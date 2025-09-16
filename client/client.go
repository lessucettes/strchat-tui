// client/client.go
package client

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/nbd-wtf/go-nostr"
)

// Client is the main struct for the strchat client.
type Client struct {
	sk string
	pk string
	n  string

	config *Config

	relays      map[string]*ManagedRelay
	relaysMu    sync.Mutex
	seenCache   *lru.Cache[string, bool]
	seenCacheMu sync.Mutex
	userContext *lru.Cache[string, UserContext]

	actionsChan <-chan UserAction
	eventsChan  chan<- DisplayEvent

	filtersCompiled []compiledPattern
	mutesCompiled   []compiledPattern

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New creates a new instance of the client.
func New(actions <-chan UserAction, events chan<- DisplayEvent) (*Client, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load configuration: %w", err)
	}

	if cfg.BlockedUsers == nil {
		cfg.BlockedUsers = []BlockedUser{}
	}

	seenCache, err := lru.New[string, bool](SeenCacheSize)
	if err != nil {
		return nil, fmt.Errorf("failed to create seen cache: %w", err)
	}

	userContextCache, err := lru.New[string, UserContext](UserContextCacheSize)
	if err != nil {
		return nil, fmt.Errorf("failed to create user context cache: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	client := &Client{
		config:      cfg,
		actionsChan: actions,
		eventsChan:  events,
		relays:      make(map[string]*ManagedRelay),
		seenCache:   seenCache,
		userContext: userContextCache,
		ctx:         ctx,
		cancel:      cancel,
	}

	client.rebuildRegexCaches()

	return client, nil
}

// Run starts the client's main event loop.
func (c *Client) Run() {
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
		c.eventsChan <- DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("No chats joined. Initial identity: %s (%s...)", c.n, c.pk[:4])}
	}

	c.sendStateUpdate()
	c.updateAllSubscriptions()

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

// handleAction dispatches user actions to their respective handlers.
func (c *Client) handleAction(action UserAction) {
	switch action.Type {
	case "SEND_MESSAGE":
		go c.publishMessage(action.Payload)
	case "ACTIVATE_VIEW":
		c.setActiveView(action.Payload)
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
	case "ADD_FILTER":
		c.addFilter(action.Payload)
	case "LIST_FILTERS":
		c.listFilters()
	case "REMOVE_FILTER":
		c.removeFilter(action.Payload)
	case "CLEAR_FILTERS":
		c.clearFilters()
	case "ADD_MUTE":
		c.addMute(action.Payload)
	case "LIST_MUTES":
		c.listMutes()
	case "REMOVE_MUTE":
		c.removeMute(action.Payload)
	case "CLEAR_MUTES":
		c.clearMutes()
	case "GET_HELP":
		c.getHelp()
	case "QUIT":
		c.shutdown()
	}
}

// shutdown gracefully stops the client.
func (c *Client) shutdown() {
	c.cancel()
	c.wg.Wait()
	select {
	case c.eventsChan <- DisplayEvent{Type: "SHUTDOWN"}:
	case <-time.After(200 * time.Millisecond):
	}
}
