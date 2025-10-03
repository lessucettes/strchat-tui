package client

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mmcloughlin/geohash"
	"github.com/nbd-wtf/go-nostr"
)

// --- State Management ---

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

	hash := sha256.Sum256([]byte(strings.Join(validMembers, "")))
	id := hex.EncodeToString(hash[:])[:6]
	name := fmt.Sprintf("Group-%s", id)

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

func (c *client) setNick(nick string) {
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

func (c *client) setActiveView(name string) {
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
