// client/client.go
package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"math/bits"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/mmcloughlin/geohash"
	"github.com/nbd-wtf/go-nostr"
)

const (
	DefaultRelayCount    = 5
	GeochatKind          = 20000
	NamedChatKind        = 23333
	SeenCacheSize        = 8192
	UserContextCacheSize = 4096
	MaxMsgLen            = 2000
	MaxChatNameLen       = 12
)

var DefaultNamedChatRelays = []string{
	"wss://relay.damus.io",
	"wss://relay.primal.net",
	"wss://nos.lol",
	"wss://offchain.pub",
	"wss://adre.su",
}

type UserAction struct {
	Type    string
	Payload string
}

// RelayInfo holds status information about a single relay connection.
type RelayInfo struct {
	URL     string
	Latency time.Duration
}

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
	Payload   any
}

type StateUpdate struct {
	Views           []View
	ActiveViewIndex int
	Nick            string
}

type UserContext struct {
	Nick    string
	Chat    string
	ShortPK string
}

type ManagedRelay struct {
	URL          string
	Relay        *nostr.Relay
	Latency      time.Duration
	subscription *nostr.Subscription
	mu           sync.Mutex
}

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

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func New(actions <-chan UserAction, events chan<- DisplayEvent) (*Client, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load configuration: %w", err)
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

	return &Client{
		config:      cfg,
		actionsChan: actions,
		eventsChan:  events,
		relays:      make(map[string]*ManagedRelay),
		seenCache:   seenCache,
		userContext: userContextCache,
		ctx:         ctx,
		cancel:      cancel,
	}, nil
}

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
	case "GET_HELP":
		c.getHelp()
	case "QUIT":
		c.shutdown()
	}
}

// --- State Management ---

func (c *Client) createGroup(payload string) {
	members := strings.Split(payload, ",")
	if len(members) < 2 {
		return
	}
	for i := range members {
		members[i] = strings.TrimSpace(members[i])
	}
	sort.Strings(members)

	hash := sha256.Sum256([]byte(strings.Join(members, "")))
	id := hex.EncodeToString(hash[:])[:6]
	name := fmt.Sprintf("Group-%s", id)

	for _, view := range c.config.Views {
		if view.Name == name {
			c.eventsChan <- DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Group with these chats already exists: '%s'", name)}
			return
		}
	}

	newView := View{Name: name, IsGroup: true, Children: members}
	c.config.Views = append(c.config.Views, newView)
	c.config.ActiveViewName = name
	c.saveConfig()

	c.sendStateUpdate()
	c.updateAllSubscriptions()
}
func (c *Client) joinChats(payload string) {
	chatNames := strings.Fields(payload)
	if len(chatNames) == 0 {
		return
	}

	var addedChats []string
	var existingChats []string

outer:
	for _, name := range chatNames {
		if geohash.Validate(name) != nil {
			normalizedName, err := normalizeAndValidateChatName(name)
			if err != nil {
				c.eventsChan <- DisplayEvent{Type: "ERROR", Content: err.Error()}
				continue outer
			}
			if utf8.RuneCountInString(normalizedName) > MaxChatNameLen {
				c.eventsChan <- DisplayEvent{
					Type:    "ERROR",
					Content: fmt.Sprintf("Chat name '%s' is too long (max %d chars).", normalizedName, MaxChatNameLen),
				}
				continue outer
			}
			if len(normalizedName) == 0 {
				continue outer
			}
			name = normalizedName
		}

		isExisting := false
		for _, view := range c.config.Views {
			if !view.IsGroup && view.Name == name {
				isExisting = true
				break
			}
		}

		if isExisting {
			existingChats = append(existingChats, name)
			continue outer
		}

		newView := View{Name: name, IsGroup: false}
		c.config.Views = append(c.config.Views, newView)
		if len(addedChats) == 0 {
			c.config.ActiveViewName = name
		}
		addedChats = append(addedChats, name)
	}

	if len(addedChats) > 0 {
		c.saveConfig()
		c.sendStateUpdate()
		c.updateAllSubscriptions()
		c.eventsChan <- DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("Joined %d new chat(s): %s. Active: %s", len(addedChats), strings.Join(addedChats, ", "), c.config.ActiveViewName)}
	} else if len(existingChats) > 0 && len(chatNames) == len(existingChats) {
		var content string
		if len(existingChats) == 1 {
			content = fmt.Sprintf("You are already in the '%s' chat.", existingChats[0])
		} else {
			content = fmt.Sprintf("You are already in all specified chats: %s.", strings.Join(existingChats, ", "))
		}
		c.eventsChan <- DisplayEvent{Type: "STATUS", Content: content}
	}
}

func (c *Client) leaveChat(chatName string) {
	var newViews []View
	for _, view := range c.config.Views {
		if !view.IsGroup && view.Name == chatName {
			continue
		}
		newViews = append(newViews, view)
	}

	finalViews := make([]View, 0, len(newViews))
	for _, view := range newViews {
		if !view.IsGroup {
			finalViews = append(finalViews, view)
			continue
		}

		var newChildren []string
		for _, child := range view.Children {
			if child != chatName {
				newChildren = append(newChildren, child)
			}
		}

		if len(newChildren) < 2 {
			continue
		}
		view.Children = newChildren
		finalViews = append(finalViews, view)
	}

	c.config.Views = finalViews
	if c.config.ActiveViewName == chatName {
		c.config.ActiveViewName = ""
	}
	c.saveConfig()
	c.sendStateUpdate()
	c.updateAllSubscriptions()
}

func (c *Client) deleteGroup(groupName string) {
	var newViews []View
	for _, view := range c.config.Views {
		if view.Name != groupName {
			newViews = append(newViews, view)
		}
	}
	c.config.Views = newViews
	if c.config.ActiveViewName == groupName {
		c.config.ActiveViewName = ""
	}
	c.saveConfig()
	c.sendStateUpdate()
	c.updateAllSubscriptions()
}

func (c *Client) deleteView(viewName string) {
	if viewName == "" {
		activeView := c.getActiveView()
		if activeView == nil {
			c.eventsChan <- DisplayEvent{Type: "ERROR", Content: "Cannot delete: there is no active chat."}
			return
		}
		viewName = activeView.Name
	}

	var viewToDelete *View
	for i := range c.config.Views {
		if c.config.Views[i].Name == viewName {
			viewToDelete = &c.config.Views[i]
			break
		}
	}

	if viewToDelete == nil {
		c.eventsChan <- DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Chat or group '%s' not found.", viewName)}
		return
	}

	if viewToDelete.IsGroup {
		c.deleteGroup(viewName)
		c.eventsChan <- DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("Group '%s' deleted.", viewName)}
	} else {
		c.leaveChat(viewName)
		c.eventsChan <- DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("Left chat '%s'.", viewName)}
	}
}

func (c *Client) setPoW(difficultyStr string) {
	difficulty, err := strconv.Atoi(strings.TrimSpace(difficultyStr))
	if err != nil {
		c.eventsChan <- DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Invalid PoW difficulty: '%s'. Must be a number.", difficultyStr)}
		return
	}

	if difficulty < 0 {
		c.eventsChan <- DisplayEvent{Type: "ERROR", Content: "PoW difficulty cannot be negative."}
		return
	}

	activeView := c.getActiveView()
	if activeView == nil {
		c.eventsChan <- DisplayEvent{Type: "ERROR", Content: "Cannot set PoW: no active chat/group."}
		return
	}

	for i := range c.config.Views {
		if c.config.Views[i].Name == activeView.Name {
			c.config.Views[i].PoW = difficulty
			break
		}
	}

	c.saveConfig()
	c.sendStateUpdate()

	if difficulty > 0 {
		c.eventsChan <- DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("PoW difficulty for %s set to %d.", activeView.Name, difficulty)}
	} else {
		c.eventsChan <- DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("PoW disabled for %s.", activeView.Name)}
	}
}

func (c *Client) setNick(nick string) {
	c.config.Nick = strings.TrimSpace(nick)
	if c.config.Nick != "" {
		c.n = c.config.Nick
		c.eventsChan <- DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("Nick set to: %s", c.n)}
	} else {
		c.n = npubToTokiPona(c.pk)
		c.eventsChan <- DisplayEvent{Type: "STATUS", Content: "Nick has been cleared."}
	}
	c.saveConfig()
	c.sendStateUpdate()
}

func (c *Client) listChats() {
	if len(c.config.Views) == 0 {
		c.eventsChan <- DisplayEvent{Type: "INFO", Content: "You are not in any chats. Use /join <chat_name> to join one."}
		return
	}

	var builder strings.Builder
	builder.WriteString("Available chats and groups:\n")
	for _, view := range c.config.Views {
		if view.IsGroup {
			builder.WriteString(fmt.Sprintf(" - %s (Group)\n", view.Name))
		} else {
			builder.WriteString(fmt.Sprintf(" - %s\n", view.Name))
		}
	}
	c.eventsChan <- DisplayEvent{Type: "INFO", Content: builder.String()}
}

func (c *Client) getActiveChat() {
	activeView := c.getActiveView()
	var content string
	if activeView != nil {
		content = fmt.Sprintf("Current active chat/group is: %s", activeView.Name)
	} else {
		content = "There is no active chat/group."
	}
	c.eventsChan <- DisplayEvent{Type: "INFO", Content: content}
}

func (c *Client) handleNickCompletion(prefix string) {
	prefix = strings.TrimPrefix(prefix, "@")
	var entries []string

	activeView := c.getActiveView()
	if activeView == nil {
		c.eventsChan <- DisplayEvent{Type: "NICK_COMPLETION_RESULT", Payload: []string{}}
		return
	}

	relevantChats := make(map[string]struct{})
	if activeView.IsGroup {
		for _, child := range activeView.Children {
			relevantChats[child] = struct{}{}
		}
	} else {
		relevantChats[activeView.Name] = struct{}{}
	}

	for _, key := range c.userContext.Keys() {
		if value, ok := c.userContext.Get(key); ok {
			if _, isActiveChat := relevantChats[value.Chat]; isActiveChat {
				if strings.HasPrefix(value.Nick, prefix) {
					entries = append(entries, fmt.Sprintf("@%s#%s ", value.Nick, value.ShortPK))
				}
			}
		}
	}

	sort.Strings(entries)
	if len(entries) > 10 {
		entries = entries[:10]
	}

	c.eventsChan <- DisplayEvent{Type: "NICK_COMPLETION_RESULT", Payload: entries}
}

func (c *Client) setActiveView(name string) {
	viewExists := false
	for _, view := range c.config.Views {
		if view.Name == name {
			viewExists = true
			break
		}
	}

	if !viewExists {
		c.eventsChan <- DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Chat or group '%s' not found.", name)}
		return
	}

	newSk := nostr.GeneratePrivateKey()
	newPk, err := nostr.GetPublicKey(newSk)
	if err != nil {
		c.eventsChan <- DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Failed to generate public key: %v", err)}
		return
	}

	c.sk = newSk
	c.pk = newPk
	if c.config.Nick != "" {
		c.n = c.config.Nick
	} else {
		c.n = npubToTokiPona(newPk)
	}

	c.config.ActiveViewName = name
	c.saveConfig()
	c.sendStateUpdate()

	c.eventsChan <- DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("Generated new ephemeral identity for this session: %s (%s...)", c.n, c.pk[:4])}
}

func (c *Client) sendStateUpdate() {
	activeIdx := -1
	for i := range c.config.Views {
		if c.config.Views[i].Name == c.config.ActiveViewName {
			activeIdx = i
			break
		}
	}
	if activeIdx == -1 && len(c.config.Views) > 0 {
		activeIdx = 0
		c.config.ActiveViewName = c.config.Views[0].Name
	}

	state := StateUpdate{
		Views:           c.config.Views,
		ActiveViewIndex: activeIdx,
		Nick:            c.n,
	}
	c.eventsChan <- DisplayEvent{Type: "STATE_UPDATE", Payload: state}
}

func (c *Client) sendRelaysUpdate() {
	c.relaysMu.Lock()
	defer c.relaysMu.Unlock()

	statuses := make([]RelayInfo, 0, len(c.relays))
	for _, mr := range c.relays {
		statuses = append(statuses, RelayInfo{
			URL:     mr.URL,
			Latency: mr.Latency,
		})
	}

	c.eventsChan <- DisplayEvent{Type: "RELAYS_UPDATE", Payload: statuses}
}

func (c *Client) saveConfig() {
	if err := c.config.Save(); err != nil {
		log.Printf("Error saving config: %v", err)
		c.eventsChan <- DisplayEvent{
			Type:    "ERROR",
			Content: fmt.Sprintf("Failed to save configuration: %v", err),
		}
	}
}

// --- Nostr Logic ---

func (c *Client) updateAllSubscriptions() {
	activeView := c.getActiveView()

	activeChats := make(map[string]struct{})
	if activeView != nil {
		if activeView.IsGroup {
			for _, child := range activeView.Children {
				activeChats[child] = struct{}{}
			}
		} else if activeView.Name != "" {
			activeChats[activeView.Name] = struct{}{}
		}
	}

	if len(activeChats) == 0 {
		c.updateRelaySubscriptions(make(map[string][]string))
		c.eventsChan <- DisplayEvent{Type: "STATUS", Content: "No active chat/group. Relay connections are inactive."}
		return
	}

	c.eventsChan <- DisplayEvent{Type: "STATUS", Content: "Updating subscriptions for active chat/group..."}

	desiredRelayToChats := make(map[string][]string)
	for chat := range activeChats {
		var relayURLs []string
		if geohash.Validate(chat) == nil {
			var err error
			relayURLs, err = ClosestRelays(chat, DefaultRelayCount)
			if err != nil || len(relayURLs) == 0 {
				relayURLs = DefaultNamedChatRelays
			}
		} else {
			relayURLs = DefaultNamedChatRelays
		}
		for _, url := range relayURLs {
			found := false
			for _, existingChat := range desiredRelayToChats[url] {
				if existingChat == chat {
					found = true
					break
				}
			}
			if !found {
				desiredRelayToChats[url] = append(desiredRelayToChats[url], chat)
			}
		}
	}

	c.updateRelaySubscriptions(desiredRelayToChats)
}

func (c *Client) updateRelaySubscriptions(desiredRelays map[string][]string) {
	c.relaysMu.Lock()
	currentRelays := make(map[string]*ManagedRelay, len(c.relays))
	for url, mr := range c.relays {
		currentRelays[url] = mr
	}
	c.relaysMu.Unlock()
	var wg sync.WaitGroup
	for url, chats := range desiredRelays {
		if mr, exists := currentRelays[url]; exists {
			wg.Add(1)
			go func(mr *ManagedRelay, chats []string) {
				defer wg.Done()
				if _, err := c.replaceSubscription(mr, chats); err != nil {
					c.eventsChan <- DisplayEvent{
						Type:    "ERROR",
						Content: fmt.Sprintf("Resubscribe failed on %s: %v", mr.URL, err),
					}
				}
			}(mr, chats)
		} else {
			wg.Add(1)
			go func(url string, chats []string) {
				defer wg.Done()
				c.manageRelayConnection(url, chats)
			}(url, chats)
		}
	}
	c.relaysMu.Lock()
	for url, mr := range c.relays {
		if _, needed := desiredRelays[url]; !needed {
			log.Printf("Disconnecting from unneeded relay: %s", url)
			mr.mu.Lock()
			if mr.subscription != nil {
				mr.subscription.Unsub()
				mr.subscription = nil
			}
			if mr.Relay != nil {
				mr.Relay.Close()
			}
			mr.mu.Unlock()
			delete(c.relays, url)
		}
	}
	c.relaysMu.Unlock()
	wg.Wait()
	c.sendRelaysUpdate()
}

func (c *Client) manageRelayConnection(url string, chats []string) {
	start := time.Now()
	relay, err := nostr.RelayConnect(c.ctx, url)
	if err != nil {
		c.eventsChan <- DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Failed to connect to %s: %v", url, err)}
		return
	}
	latency := time.Since(start)

	c.eventsChan <- DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("Connected to %s (%dms)", url, latency.Milliseconds())}

	mr := &ManagedRelay{
		URL:     url,
		Relay:   relay,
		Latency: latency,
	}

	c.relaysMu.Lock()
	if _, exists := c.relays[url]; exists {
		c.relaysMu.Unlock()
		relay.Close()
		return
	}
	c.relays[url] = mr
	c.relaysMu.Unlock()
	c.sendRelaysUpdate()

	if _, err := c.replaceSubscription(mr, chats); err != nil {
		c.eventsChan <- DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Subscribe failed on %s: %v", url, err)}
		c.relaysMu.Lock()
		delete(c.relays, url)
		c.relaysMu.Unlock()
		relay.Close()
		c.sendRelaysUpdate()
		return
	}

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.listenForEvents(mr)
	}()
}

func (c *Client) replaceSubscription(mr *ManagedRelay, chats []string) (bool, error) {
	mr.mu.Lock()
	oldChats := mrCurrentChatsLocked(mr.subscription)
	mr.mu.Unlock()

	if sameStringSet(oldChats, chats) {
		return false, nil
	}

	now := nostr.Now()
	filters := make(nostr.Filters, 0, len(chats))
	for _, ch := range chats {
		since := now
		if geohash.Validate(ch) == nil {
			filters = append(filters, nostr.Filter{
				Kinds: []int{GeochatKind},
				Tags:  nostr.TagMap{"g": []string{ch}},
				Since: &since,
			})
		} else {
			filters = append(filters, nostr.Filter{
				Kinds: []int{NamedChatKind},
				Tags:  nostr.TagMap{"d": []string{ch}},
				Since: &since,
			})
		}
	}

	newSub, err := mr.Relay.Subscribe(c.ctx, filters)
	if err != nil {
		return false, fmt.Errorf("subscribe failed: %w", err)
	}

	mr.mu.Lock()
	oldSub := mr.subscription
	mr.subscription = newSub
	mr.mu.Unlock()

	if oldSub != nil {
		oldSub.Unsub()
	}
	log.Printf("Updated subscription for %s with %d chat(s)", mr.URL, len(chats))

	c.sendRelaysUpdate()

	return true, nil
}

func (c *Client) listenForEvents(mr *ManagedRelay) {
	log.Printf("Listener started for relay: %s", mr.URL)
	defer log.Printf("Listener stopped for relay: %s", mr.URL)

	for {
		if c.ctx.Err() != nil {
			return
		}

		mr.mu.Lock()
		sub := mr.subscription
		mr.mu.Unlock()

		if sub == nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		select {
		case <-c.ctx.Done():
			return

		case ev, ok := <-sub.Events:
			if !ok {
				log.Printf("Subscription object for %s is closed, will wait for a new one.", mr.URL)
				mr.mu.Lock()
				if mr.subscription == sub {
					mr.subscription = nil
				}
				mr.mu.Unlock()
				continue
			}
			if ev == nil {
				continue
			}
			c.processEvent(ev, mr.URL)
		}
	}
}

func (c *Client) processEvent(ev *nostr.Event, relayURL string) {
	c.seenCacheMu.Lock()
	if c.seenCache.Contains(ev.ID) {
		c.seenCacheMu.Unlock()
		return
	}
	c.seenCache.Add(ev.ID, true)
	c.seenCacheMu.Unlock()

	var eventChat string
	if gTag := ev.Tags.Find("g"); len(gTag) > 1 {
		eventChat = gTag[1]
	} else if dTag := ev.Tags.Find("d"); len(dTag) > 1 {
		eventChat = dTag[1]
	}

	if eventChat == "" {
		return
	}

	activeView := c.getActiveView()
	if activeView != nil {
		isRelevantToActiveView := false
		if activeView.IsGroup {
			for _, child := range activeView.Children {
				if child == eventChat {
					isRelevantToActiveView = true
					break
				}
			}
		} else {
			if activeView.Name == eventChat {
				isRelevantToActiveView = true
			}
		}

		if isRelevantToActiveView {
			requiredPoW := activeView.PoW
			if !IsPoWValid(ev, requiredPoW) {
				log.Printf("Dropped event %s from %s for failing PoW check (required: %d)", ev.ID[len(ev.ID)-4:], eventChat, requiredPoW)
				return
			}
		}
	}

	nick := npubToTokiPona(ev.PubKey)
	spk := ev.PubKey[:4]
	if nickTag := ev.Tags.Find("n"); len(nickTag) > 1 {
		if s := sanitizeString(nickTag[1]); s != "" {
			nick = s
		}
		spk = ev.PubKey[len(ev.PubKey)-4:]
	}

	c.userContext.Add(ev.PubKey, UserContext{
		Nick:    nick,
		Chat:    eventChat,
		ShortPK: spk,
	})

	timestamp := time.Unix(int64(ev.CreatedAt), 0).Format("15:04:05")

	content := truncateString(ev.Content, MaxMsgLen)
	content = sanitizeString(content)

	c.eventsChan <- DisplayEvent{
		Type:      "NEW_MESSAGE",
		Timestamp: timestamp,
		Nick:      nick,
		Color:     pubkeyToColor(ev.PubKey),
		PubKey:    spk,
		Content:   content,
		ID:        ev.ID[len(ev.ID)-4:],
		Chat:      eventChat,
		RelayURL:  relayURL,
	}
}

func (c *Client) publishMessage(message string) {
	var targetChat string
	var targetPubKey string
	if strings.HasPrefix(message, "@") {
		var matchedReplyTag string
		for _, pk := range c.userContext.Keys() {
			if ctx, ok := c.userContext.Get(pk); ok {
				replyTag := fmt.Sprintf("@%s#%s", ctx.Nick, ctx.ShortPK)
				if strings.HasPrefix(message, replyTag) {
					if len(replyTag) > len(matchedReplyTag) {
						matchedReplyTag = replyTag
						targetPubKey = pk
						targetChat = ctx.Chat
					}
				}
			}
		}

		if targetPubKey == "" {
			c.eventsChan <- DisplayEvent{Type: "ERROR", Content: "Could not find a known user matching your message prefix."}
			return
		}
	} else {
		activeView := c.getActiveView()
		if activeView == nil {
			c.eventsChan <- DisplayEvent{Type: "ERROR", Content: "No active chat/group to send message to."}
			return
		}
		if activeView.IsGroup {
			c.eventsChan <- DisplayEvent{Type: "ERROR", Content: "Broadcasting to a group is disabled. Use @nick to send a message."}
			return
		}
		if activeView.Name == "" {
			c.eventsChan <- DisplayEvent{Type: "ERROR", Content: "The active chat is invalid."}
			return
		}
		targetChat = activeView.Name
	}

	var kind int
	var tagKey string
	var relayURLs []string
	if geohash.Validate(targetChat) == nil {
		kind = GeochatKind
		tagKey = "g"
		var err error
		relayURLs, err = ClosestRelays(targetChat, DefaultRelayCount)
		if err != nil || len(relayURLs) == 0 {
			c.eventsChan <- DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("No relays found for chat %s", targetChat)}
			return
		}
	} else {
		kind = NamedChatKind
		tagKey = "d"
		relayURLs = DefaultNamedChatRelays
	}

	tags := nostr.Tags{{tagKey, targetChat}}
	if targetPubKey != "" {
		tags = append(tags, nostr.Tag{"p", targetPubKey})
	}

	activeView := c.getActiveView()
	if activeView == nil {
		c.eventsChan <- DisplayEvent{Type: "ERROR", Content: "Cannot determine PoW: No active chat/group."}
		return
	}
	requiredPoW := activeView.PoW

	c.relaysMu.Lock()
	var relaysForPublishing []*ManagedRelay
	for _, url := range relayURLs {
		if r, ok := c.relays[url]; ok {
			relaysForPublishing = append(relaysForPublishing, r)
		}
	}
	c.relaysMu.Unlock()

	if len(relaysForPublishing) == 0 {
		c.eventsChan <- DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Not connected to any suitable relays for chat %s", targetChat)}
		return
	}

	ev := c.createEvent(message, kind, tags, requiredPoW)
	if ev.ID == "" { // PoW was cancelled
		return
	}

	c.publish(ev, targetChat, relaysForPublishing)
}

func (c *Client) publish(ev nostr.Event, targetChat string, relaysForPublishing []*ManagedRelay) {
	sort.Slice(relaysForPublishing, func(i, j int) bool {
		return relaysForPublishing[i].Latency < relaysForPublishing[j].Latency
	})

	var wg sync.WaitGroup
	successCount := 0
	var errorMessages []string
	var mu sync.Mutex

	for _, r := range relaysForPublishing {
		wg.Add(1)
		go func(r *ManagedRelay) {
			defer wg.Done()
			if err := r.Relay.Publish(c.ctx, ev); err == nil {
				mu.Lock()
				successCount++
				mu.Unlock()
			} else {
				mu.Lock()
				errorMessages = append(errorMessages, fmt.Sprintf("%s: %v", r.URL, err))
				mu.Unlock()
			}
		}(r)
	}
	wg.Wait()

	status := fmt.Sprintf("Event %s sent to %d/%d relays for chat %s.", ev.ID[len(ev.ID)-4:], successCount, len(relaysForPublishing), targetChat)
	if len(errorMessages) > 0 {
		status += " Errors: " + strings.Join(errorMessages, ", ")
	}
	c.eventsChan <- DisplayEvent{Type: "STATUS", Content: status}
}

func (c *Client) createEvent(message string, kind int, tags nostr.Tags, difficulty int) nostr.Event {
	baseTags := make(nostr.Tags, 0, len(tags)+2)
	baseTags = append(baseTags, tags...)
	baseTags = append(baseTags, nostr.Tag{"n", c.n})

	ev := nostr.Event{
		CreatedAt: nostr.Now(),
		PubKey:    c.pk,
		Content:   message,
		Kind:      kind,
		Tags:      baseTags,
	}

	if difficulty > 0 {
		c.eventsChan <- DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("Calculating Proof-of-Work (difficulty %d)...", difficulty)}

		powTags := append(baseTags, nostr.Tag{"nonce", "0", strconv.Itoa(difficulty)})
		nonceTagIndex := len(powTags) - 1
		ev.Tags = powTags

		var nonceCounter uint64 = 0
		for {
			powTags[nonceTagIndex][1] = strconv.FormatUint(nonceCounter, 10)
			ev.ID = ev.GetID()

			if countLeadingZeroBits(ev.ID) >= difficulty {
				break
			}

			nonceCounter++

			if nonceCounter&0xFFFF == 0 {
				select {
				case <-c.ctx.Done():
					c.eventsChan <- DisplayEvent{Type: "STATUS", Content: "PoW calculation cancelled."}
					return nostr.Event{}
				default:
				}
			}
		}
	}

	_ = ev.Sign(c.sk)
	return ev
}

func (c *Client) getActiveView() *View {
	for i := range c.config.Views {
		if c.config.Views[i].Name == c.config.ActiveViewName {
			return &c.config.Views[i]
		}
	}
	if len(c.config.Views) > 0 {
		return &c.config.Views[0]
	}
	return nil
}

func (c *Client) shutdown() {
	c.cancel()
	c.wg.Wait()
	select {
	case c.eventsChan <- DisplayEvent{Type: "SHUTDOWN"}:
	case <-time.After(200 * time.Millisecond):
	}
}

// --- helpers ---

var hexToLeadingZeros [256]int

func init() {
	for i := 0; i < 256; i++ {
		char := byte(i)
		var val uint64
		if char >= '0' && char <= '9' {
			val, _ = strconv.ParseUint(string(char), 16, 4)
		} else if char >= 'a' && char <= 'f' {
			val, _ = strconv.ParseUint(string(char), 16, 4)
		} else if char >= 'A' && char <= 'F' {
			val, _ = strconv.ParseUint(string(char), 16, 4)
		} else {
			hexToLeadingZeros[i] = -1
			continue
		}
		if val == 0 {
			hexToLeadingZeros[i] = 4
		} else {
			hexToLeadingZeros[i] = bits.LeadingZeros8(uint8(val << 4))
		}
	}
}

func countLeadingZeroBits(hexString string) int {
	count := 0
	for i := 0; i < len(hexString); i++ {
		char := hexString[i]
		zeros := hexToLeadingZeros[char]

		if zeros == -1 {
			return count
		}

		count += zeros
		if zeros != 4 {
			break
		}
	}
	return count
}

func IsPoWValid(event *nostr.Event, minDifficulty int) bool {
	if minDifficulty <= 0 {
		return true
	}

	nonceTag := event.Tags.FindLast("nonce")
	if len(nonceTag) < 3 {
		return false
	}

	claimedDifficulty, err := strconv.Atoi(strings.TrimSpace(nonceTag[2]))
	if err != nil || claimedDifficulty < minDifficulty {
		return false
	}

	actualDifficulty := countLeadingZeroBits(event.ID)
	return actualDifficulty >= claimedDifficulty
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[string]struct{}, len(a))
	for _, s := range a {
		m[s] = struct{}{}
	}
	for _, s := range b {
		if _, ok := m[s]; !ok {
			return false
		}
	}
	return true
}

func mrCurrentChatsLocked(sub *nostr.Subscription) []string {
	if sub == nil {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	for _, f := range sub.Filters {
		tagsToCkeck := [][]string{}
		if gTags, ok := f.Tags["g"]; ok {
			tagsToCkeck = append(tagsToCkeck, gTags)
		}
		if dTags, ok := f.Tags["d"]; ok {
			tagsToCkeck = append(tagsToCkeck, dTags)
		}

		for _, tagSet := range tagsToCkeck {
			for _, ch := range tagSet {
				if _, exists := seen[ch]; !exists {
					seen[ch] = struct{}{}
					out = append(out, ch)
				}
			}
		}
	}
	return out
}

func pubkeyToColor(pubkey string) string {
	hackerPalette := []string{"[#00ff00]", "[#33ccff]", "[#ff00ff]", "[#ffff00]", "[#6600ff]", "[#5fafd7]"}
	hash := sha256.Sum256([]byte(pubkey))
	return hackerPalette[int(hash[0])%len(hackerPalette)]
}

func truncateString(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}

	runesCounted := 0
	for i := range s {
		runesCounted++
		if runesCounted > maxRunes {
			return s[:i] + "..."
		}
	}

	return s
}

func normalizeAndValidateChatName(name string) (string, error) {
	normalized := strings.ToLower(name)

	var builder strings.Builder
	builder.Grow(len(normalized))

	var lastWasDash bool
	for _, r := range normalized {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			builder.WriteRune(r)
			lastWasDash = false
		} else if unicode.IsSpace(r) || r == '-' {
			if !lastWasDash {
				builder.WriteRune('-')
				lastWasDash = true
			}
		} else {
			return "", fmt.Errorf("chat name contains invalid character: '%c'", r)
		}
	}

	return strings.Trim(builder.String(), "-"), nil
}

func sanitizeString(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	var prevWasRI bool
	for _, r := range s {
		if r < 32 || r == 127 {
			continue
		}
		if r < 128 {
			b.WriteRune(r)
			prevWasRI = false
			continue
		}

		if unicode.Is(unicode.M, r) {
			b.WriteRune('?')
			continue
		}

		if !unicode.IsPrint(r) {
			continue
		}

		if r >= 0x1F1E6 && r <= 0x1F1FF {
			if prevWasRI {
				b.WriteRune('?')
				prevWasRI = false
				continue
			}
			prevWasRI = true
			continue
		}

		prevWasRI = false
		b.WriteRune(r)
	}
	return b.String()
}

func npubToTokiPona(npub string) string {
	hash := sha256.Sum256([]byte(npub))
	return fmt.Sprintf("%s-%s-%s",
		tokiPonaNouns[int(hash[0])%len(tokiPonaNouns)],
		tokiPonaNouns[int(hash[1])%len(tokiPonaNouns)],
		tokiPonaNouns[int(hash[2])%len(tokiPonaNouns)],
	)
}

var tokiPonaNouns = []string{
	"ijo", "ilo", "insa", "jan", "jelo", "jo", "kala", "kalama", "kasi", "ken",
	"kili", "kiwen", "ko", "kon", "kulupu", "lape", "laso", "lawa", "len", "lili",
	"linja", "lipu", "loje", "luka", "lukin", "lupa", "ma", "mama", "mani", "meli",
	"mije", "moku", "moli", "monsi", "mun", "musi", "mute", "nanpa", "nasin", "nena",
	"nimi", "noka", "oko", "olin", "open", "pakala", "pali", "palisa", "pan", "pilin",
	"pipi", "poki", "pona", "selo", "sewi", "sijelo", "sike", "sitelen", "sona", "soweli",
	"suli", "suno", "supa", "suwi", "telo", "tenpo", "toki", "tomo", "unpa", "uta",
	"utala", "waso", "wawa", "weka", "wile",
}

func (c *Client) getHelp() {
	helpText := "COMMANDS:\n" +
		"[blue]*[-] /join <chat1> [chat2]... - Joins one or more chats. (Alias: /j)\n" +
		"[blue]*[-] /set [name|names...] - Without args: shows active chat. With one name: activates a chat/group. With multiple names: creates a group. (Alias: /s)\n" +
		"[blue]*[-] /list - Lists all your chats and groups. (Alias: /l)\n" +
		"[blue]*[-] /nick [new_nick] - Sets or clears your nickname. (Alias: /n)\n" +
		"[blue]*[-] /pow [number] - Sets Proof-of-Work difficulty for the active chat/group. 0 to disable. (Alias: /p)\n" +
		"[blue]*[-] /del [name] - Deletes a chat/group. If no name, deletes the active chat/group. (Alias: /d)\n" +
		"[blue]*[-] /quit - Exits the application. (Alias: /q)"

	c.eventsChan <- DisplayEvent{Type: "INFO", Content: helpText}
}
