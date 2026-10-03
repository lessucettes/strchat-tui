package client

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/nbd-wtf/go-nostr"
)

// Run is the only owner of configuration, identities, caches and relay handles.
// Workers receive immutable snapshots and return results through bounded channels.
type client struct {
	sk, pk, n                      string
	config                         *config
	chatKeys                       map[string]chatSession
	actionsChan                    <-chan UserAction
	eventsChan                     chan<- DisplayEvent
	ctx                            context.Context
	cancel                         context.CancelFunc
	wg                             sync.WaitGroup
	relays                         map[string]*managedRelay
	incoming                       chan relayEvent
	outgoing                       chan publishJob
	results                        chan publishResult
	seenCache                      *lru.Cache[string, bool]
	userContext                    *lru.Cache[string, userContext]
	filtersCompiled, mutesCompiled []compiledPattern
}

func New(actions <-chan UserAction, events chan<- DisplayEvent) (*client, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load configuration: %w", err)
	}
	return newClient(cfg, actions, events), nil
}

func newClient(cfg *config, actions <-chan UserAction, events chan<- DisplayEvent) *client {
	ctx, cancel := context.WithCancel(context.Background())
	seen, _ := lru.New[string, bool](seenCacheSize)
	users, _ := lru.New[string, userContext](userContextCacheSize)
	c := &client{config: cfg, actionsChan: actions, eventsChan: events, ctx: ctx, cancel: cancel,
		chatKeys: make(map[string]chatSession), relays: make(map[string]*managedRelay),
		incoming: make(chan relayEvent, 256), outgoing: make(chan publishJob, 8), results: make(chan publishResult, 8),
		seenCache: seen, userContext: users, n: cfg.Nick}
	c.rebuildRegexCaches()
	return c
}

// Stop is safe concurrently with Run, including while the display is blocked.
// The caller joins Run before releasing its event channel.
func (c *client) Stop() { c.cancel() }

func (c *client) emit(event DisplayEvent) {
	select {
	case c.eventsChan <- event:
	case <-c.ctx.Done():
	}
}

func (c *client) Run() {
	defer c.shutdown()
	if c.ctx.Err() != nil {
		return
	}
	c.sk = c.config.PrivateKey
	if c.sk == "" {
		c.sk = nostr.GeneratePrivateKey()
		c.config.PrivateKey = c.sk
		c.saveConfig()
	}
	c.pk, _ = nostr.GetPublicKey(c.sk)
	// Start the transport before the first display write: emit blocks while the
	// display channel is full, and relay workers must not depend on the UI
	// draining its queue, or a saturated display leaves the client with no dial,
	// no subscription and no traffic. updateAllSubscriptions emits its relay
	// snapshot only after the workers exist.
	c.wg.Go(c.publishLoop)
	c.updateAllSubscriptions()
	if view := c.getActiveView(); view != nil {
		c.setActiveView(view.Name)
	} else {
		if c.n == "" {
			c.n = npubToTokiPona(c.pk)
		}
		c.emit(DisplayEvent{Type: "INFO", Content: "No chats joined. Use /join <chat> and /help. Public messages are not encrypted."})
	}
	c.sendStateUpdate()
	c.emit(DisplayEvent{Type: "THEME_UPDATE", Content: c.config.Theme})
	for c.ctx.Err() == nil {
		select {
		case <-c.ctx.Done():
			return
		case action, ok := <-c.actionsChan:
			if !ok {
				return
			}
			c.handleAction(action)
		case incoming := <-c.incoming:
			worker := incoming.worker
			if c.relays[worker.url] != worker {
				continue
			}
			if incoming.event != nil {
				c.processEvent(incoming.event, worker.url)
			} else {
				worker.connected = incoming.connected
				worker.latency = incoming.latency
				c.sendRelaysUpdate()
			}
		case result := <-c.results:
			if result.accepted > 0 {
				c.processEvent(&result.event, "local")
			}
			c.emit(DisplayEvent{Type: result.kind, Content: result.message})
		}
	}
}

func (c *client) shutdown() {
	c.cancel()
	c.wg.Wait()
	select {
	case c.eventsChan <- DisplayEvent{Type: "SHUTDOWN"}:
	default:
	}
}

func (c *client) handleAction(action UserAction) {
	switch action.Type {
	case "SEND_MESSAGE":
		c.publishMessage(action.Payload)
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
	case "SET_THEME":
		c.setTheme(action.Payload)
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
		c.Stop()
	}
}

func (c *client) manageAnchors(payload string) {
	args := strings.Fields(payload)
	if len(args) == 0 {
		var text strings.Builder
		text.WriteString("Configured relays (no automatic discovery):\n")
		for i, url := range c.config.AnchorRelays {
			fmt.Fprintf(&text, "[%d] %s\n", i+1, url)
		}
		text.WriteString("Use /relay <url> to add, /relay <number> to remove. Empty list uses defaults.")
		c.emit(DisplayEvent{Type: "INFO", Content: text.String()})
		return
	}
	if len(args) == 1 {
		if index, err := strconv.Atoi(args[0]); err == nil {
			if index < 1 || index > len(c.config.AnchorRelays) {
				c.emit(DisplayEvent{Type: "ERROR", Content: "Invalid relay index."})
				return
			}
			c.config.AnchorRelays = append(c.config.AnchorRelays[:index-1], c.config.AnchorRelays[index:]...)
			c.saveConfig()
			c.updateAllSubscriptions()
			return
		}
	}
	existing := make(map[string]bool)
	for _, url := range c.config.AnchorRelays {
		existing[url] = true
	}
	for _, raw := range args {
		url, err := normalizeRelayURL(raw)
		if err != nil {
			c.emit(DisplayEvent{Type: "ERROR", Content: err.Error()})
			continue
		}
		if existing[url] {
			continue
		}
		if len(c.config.AnchorRelays) >= maxActiveRelays {
			c.emit(DisplayEvent{Type: "ERROR", Content: "Relay limit reached (12). Remove an unused relay first."})
			break
		}
		c.config.AnchorRelays = append(c.config.AnchorRelays, url)
		existing[url] = true
	}
	c.saveConfig()
	c.updateAllSubscriptions()
}
