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

// client is the main struct for the strchat client.
type client struct {
	sk              string
	pk              string
	n               string
	config          *config
	relays          map[string]*managedRelay
	relaysMu        sync.Mutex
	seenCache       *lru.Cache[string, bool]
	seenCacheMu     sync.Mutex
	userContext     *lru.Cache[string, userContext]
	actionsChan     <-chan UserAction
	eventsChan      chan<- DisplayEvent
	filtersCompiled []compiledPattern
	mutesCompiled   []compiledPattern
	orderMu         sync.Mutex
	orderBuf        map[string][]orderItem
	orderTimers     map[string]*time.Timer
	ctx             context.Context
	cancel          context.CancelFunc
	wg              sync.WaitGroup
}

// New creates a new instance of the client.
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

	ctx, cancel := context.WithCancel(context.Background())

	client := &client{
		config:      cfg,
		actionsChan: actions,
		eventsChan:  events,
		relays:      make(map[string]*managedRelay),
		seenCache:   seenCache,
		userContext: userContextCache,
		orderBuf:    make(map[string][]orderItem),
		orderTimers: make(map[string]*time.Timer),
		ctx:         ctx,
		cancel:      cancel,
	}

	client.rebuildRegexCaches()

	return client, nil
}

// Run starts the client's main event loop.
func (c *client) Run() {
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

// handleAction dispatches user actions to their respective handlers.
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
	case "GET_HELP":
		c.getHelp()
	case "QUIT":
		c.shutdown()
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
