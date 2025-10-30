package client

import (
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mmcloughlin/geohash"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

// Chat/Group View

func (c *client) joinChats(payload string) {
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
			if utf8.RuneCountInString(normalizedName) > maxChatNameLen {
				c.eventsChan <- DisplayEvent{
					Type:    "ERROR",
					Content: fmt.Sprintf("Chat name '%s' is too long (max %d chars).", normalizedName, maxChatNameLen),
				}
				continue outer
			}
			if len(normalizedName) == 0 {
				continue outer
			}
			name = normalizedName
		}

		for _, v := range c.config.Views {
			if !v.IsGroup && v.Name == name {
				existingChats = append(existingChats, name)
				continue outer
			}
		}

		newView := View{Name: name, IsGroup: false}
		c.config.Views = append(c.config.Views, newView)
		addedChats = append(addedChats, name)
	}

	switch {
	case len(addedChats) > 0:
		active := addedChats[0]
		c.setActiveView(active)
		c.updateAllSubscriptions()
	case len(existingChats) > 0:
		var content string
		if len(existingChats) == 1 {
			content = fmt.Sprintf("You are already in the '%s' chat.", existingChats[0])
		} else {
			content = fmt.Sprintf("You are already in all specified chats: %s.", strings.Join(existingChats, ", "))
		}
		c.eventsChan <- DisplayEvent{Type: "STATUS", Content: content}
	}
}

func (c *client) createGroup(payload string) {
	existingChats := make(map[string]struct{})
	for _, view := range c.config.Views {
		if !view.IsGroup {
			existingChats[view.Name] = struct{}{}
		}
	}

	rawMembers := strings.Split(payload, ",")
	validMembers := make([]string, 0)
	notFoundChats := make([]string, 0)
	seenMembers := make(map[string]struct{})

	for _, member := range rawMembers {
		trimmedMember := strings.TrimSpace(member)
		if trimmedMember == "" {
			continue
		}

		if _, seen := seenMembers[trimmedMember]; seen {
			continue
		}
		seenMembers[trimmedMember] = struct{}{}

		if _, exists := existingChats[trimmedMember]; exists {
			validMembers = append(validMembers, trimmedMember)
		} else {
			notFoundChats = append(notFoundChats, trimmedMember)
		}
	}

	if len(notFoundChats) > 0 {
		c.eventsChan <- DisplayEvent{
			Type:    "ERROR",
			Content: fmt.Sprintf("Cannot create group. The following chats were not found: %s", strings.Join(notFoundChats, ", ")),
		}
		return
	}

	if len(validMembers) < 2 {
		c.eventsChan <- DisplayEvent{Type: "ERROR", Content: "A group requires at least two unique, existing chats."}
		return
	}

	sort.Strings(validMembers)

	name := groupName(validMembers)

	for _, view := range c.config.Views {
		if view.Name == name {
			c.eventsChan <- DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Group with these chats already exists: '%s'", name)}
			return
		}
	}

	newView := View{Name: name, IsGroup: true, Children: validMembers}
	c.config.Views = append(c.config.Views, newView)
	c.config.ActiveViewName = name
	c.saveConfig()

	c.sendStateUpdate()
	c.updateAllSubscriptions()
}

func (c *client) leaveChat(chatName string) {
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

	delete(c.chatKeys, chatName)
}

func (c *client) deleteGroup(groupName string) {
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

func (c *client) deleteView(viewName string) {
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

// Settings

func (c *client) setNick(nick string) {
	nick = strings.TrimSpace(nick)
	c.config.Nick = nick

	if nick != "" {
		c.n = nick
		c.eventsChan <- DisplayEvent{
			Type:    "STATUS",
			Content: fmt.Sprintf("Nick set to: %s", c.n),
		}
		for name, session := range c.chatKeys {
			session.nick = c.n
			session.customNick = true
			c.chatKeys[name] = session
		}
	} else {
		c.n = npubToTokiPona(c.pk)
		for name, session := range c.chatKeys {
			session.nick = npubToTokiPona(session.pubKey)
			session.customNick = false
			c.chatKeys[name] = session
		}
		c.eventsChan <- DisplayEvent{Type: "STATUS", Content: "Nick has been cleared."}
	}

	c.saveConfig()
	c.sendStateUpdate()
}

func (c *client) setPoW(difficultyStr string) {
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

// Read-only & Completions

func (c *client) listChats() {
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

func (c *client) getActiveChat() {
	activeView := c.getActiveView()
	var content string
	if activeView != nil {
		content = fmt.Sprintf("Current active chat/group is: %s", activeView.Name)
	} else {
		content = "There is no active chat/group."
	}
	c.eventsChan <- DisplayEvent{Type: "INFO", Content: content}
}

func (c *client) getHelp() {
	helpText := "COMMANDS:\n" +
		"* /join <chat1> [chat2]... - Joins one or more chats. (Alias: /j)\n" +
		"* /set [name|names...] - Without args: shows active chat. With one name: activates a chat/group. With multiple names: creates a group. (Alias: /s)\n" +
		"* /list - Lists all your chats and groups. (Alias: /l)\n" +
		"* /del [name] - Deletes a chat/group. If no name, deletes the active chat/group. (Alias: /d)\n" +
		"* /nick [new_nick] - Sets or clears your nickname. (Alias: /n)\n" +
		"* /pow [number] - Sets Proof-of-Work difficulty for the active chat/group. 0 to disable. (Alias: /p)\n" +
		"* /relay [<num>|url1...] - List, remove (#), or add anchor relays. (Alias: /r)\n" +
		"* /block [@nick] - Blocks a user. Without nick, lists blocked users. (Alias: /b)\n" +
		"* /unblock [<num>|@nick|pubkey] - Unblocks a user. Without args, lists blocked users. (Alias: /ub)\n" +
		"* /filter [word|regex|<num>] - Adds a filter. Without args, lists filters. With number, toggles off/on. (Alias: /f)\n" +
		"* /unfilter [<num>] - Removes a filter by number. Without args, clears all. (Alias: /uf)\n" +
		"* /mute [word|regex|<num>] - Adds a mute. Without args, lists mutes. With number, toggles off/on. (Alias: /m)\n" +
		"* /unmute [<num>] - Removes a mute by number. Without args, clears all. (Alias: /um)\n" +
		"* /quit - Exits the application. (Alias: /q)"

	c.eventsChan <- DisplayEvent{Type: "INFO", Content: helpText}
}

func (c *client) handleNickCompletion(prefix string) {
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
			if _, isActiveChat := relevantChats[value.chat]; isActiveChat {
				if strings.HasPrefix(value.nick, prefix) {
					entries = append(entries, fmt.Sprintf("@%s#%s ", value.nick, value.shortPubKey))
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

// Core State Primitives

func (c *client) setActiveView(name string) {
	viewExists := false
	var view *View
	for i := range c.config.Views {
		if c.config.Views[i].Name == name {
			viewExists = true
			view = &c.config.Views[i]
			break
		}
	}

	if !viewExists {
		c.eventsChan <- DisplayEvent{
			Type:    "ERROR",
			Content: fmt.Sprintf("Chat or group '%s' not found.", name),
		}
		return
	}

	if !view.IsGroup {
		sk := nostr.GeneratePrivateKey()
		pk, _ := nostr.GetPublicKey(sk)

		nick := c.config.Nick
		custom := false
		if nick == "" {
			nick = npubToTokiPona(pk)
		} else {
			custom = true
		}

		c.chatKeys[name] = chatSession{
			privKey:    sk,
			pubKey:     pk,
			nick:       nick,
			customNick: custom,
		}

		npub, _ := nip19.EncodePublicKey(pk)
		c.eventsChan <- DisplayEvent{
			Type: "STATUS",
			Content: fmt.Sprintf("Generated ephemeral identity for chat '%s': %s (%s)",
				view.Name, npub, nick),
		}
	}

	c.config.ActiveViewName = name
	c.saveConfig()
	c.sendStateUpdate()
}

func (c *client) getActiveView() *View {
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

// Helpers

func (c *client) sendStateUpdate() {
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

	if len(c.config.Views) == 0 || activeIdx == -1 {
		c.eventsChan <- DisplayEvent{Type: "STATE_UPDATE", Payload: state}
		return
	}

	if c.config.Nick != "" {
		state.Nick = c.config.Nick
	} else {
		v := c.config.Views[activeIdx]
		if v.IsGroup {
			state.Nick = npubToTokiPona(c.pk)
		} else if s, ok := c.chatKeys[v.Name]; ok && s.nick != "" {
			state.Nick = s.nick
		} else {
			state.Nick = npubToTokiPona(c.pk)
		}
	}

	c.eventsChan <- DisplayEvent{Type: "STATE_UPDATE", Payload: state}
}

func (c *client) saveConfig() {
	if err := c.config.save(); err != nil {
		log.Printf("Error saving config: %v", err)
		c.eventsChan <- DisplayEvent{
			Type:    "ERROR",
			Content: fmt.Sprintf("Failed to save configuration: %v", err),
		}
	}
}
