package client

import (
	"fmt"
	"log"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
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
		if !isGeohashName(name) {
			normalizedName, err := normalizeAndValidateChatName(name)
			if err != nil {
				c.emit(DisplayEvent{Type: "ERROR", Content: err.Error()})
				continue outer
			}
			if utf8.RuneCountInString(normalizedName) > maxChatNameLen {
				c.emit(DisplayEvent{
					Type:    "ERROR",
					Content: fmt.Sprintf("Chat name '%s' is too long (max %d chars).", normalizedName, maxChatNameLen),
				})
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
		c.emit(DisplayEvent{Type: "STATUS", Content: content})
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
		c.emit(DisplayEvent{
			Type:    "ERROR",
			Content: fmt.Sprintf("Cannot create group. The following chats were not found: %s", strings.Join(notFoundChats, ", ")),
		})
		return
	}

	if len(validMembers) < 2 {
		c.emit(DisplayEvent{Type: "ERROR", Content: "A group requires at least two unique, existing chats."})
		return
	}

	sort.Strings(validMembers)

	name := groupName(validMembers)

	for _, view := range c.config.Views {
		if view.Name == name {
			c.emit(DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Group with these chats already exists: '%s'", name)})
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

func (c *client) selectGroupChat(group, chat string) {
	for i := range c.config.Views {
		view := &c.config.Views[i]
		if !view.IsGroup || view.Name != group || !slices.Contains(view.Children, chat) {
			continue
		}
		if view.SelectedChat == chat {
			return
		}
		view.SelectedChat = chat
		if err := c.config.save(); err != nil {
			c.emit(DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Failed to save group chat selection; it will only be remembered for this session: %v", err)})
		}
		c.sendStateUpdate()
		return
	}
	c.emit(DisplayEvent{Type: "ERROR", Content: "Cannot select conversation: the group or member no longer exists."})
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
	if session, ok := c.chatKeys[chatName]; ok {
		c.retiredKeys.Add(session.pubKey, true)
	}
	delete(c.chatKeys, chatName)
	c.activateRemainingView()
}

func (c *client) deleteGroup(groupName string) {
	var newViews []View
	for _, view := range c.config.Views {
		if view.Name != groupName {
			newViews = append(newViews, view)
		}
	}
	c.config.Views = newViews
	c.activateRemainingView()
}

// A fallback chat needs the same session identity and persisted selection as an
// explicit view switch. Merely sending a snapshot would select it without a key.
func (c *client) activateRemainingView() {
	if view := c.getActiveView(); view != nil {
		c.setActiveView(view.Name)
	} else {
		c.config.ActiveViewName = ""
		c.saveConfig()
		c.sendStateUpdate()
	}
	c.updateAllSubscriptions()
}

func (c *client) deleteView(viewName string) {
	if viewName == "" {
		activeView := c.getActiveView()
		if activeView == nil {
			c.emit(DisplayEvent{Type: "ERROR", Content: "Cannot delete: there is no active chat."})
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
		c.emit(DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Chat or group '%s' not found.", viewName)})
		return
	}

	if viewToDelete.IsGroup {
		c.deleteGroup(viewName)
		c.emit(DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("Group '%s' deleted.", viewName)})
	} else {
		c.leaveChat(viewName)
		c.emit(DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("Left chat '%s'.", viewName)})
	}
}

// Settings

func (c *client) setNick(nick string) {
	nick = strings.TrimSpace(nick)
	c.config.Nick = nick

	if nick != "" {
		c.emit(DisplayEvent{
			Type:    "STATUS",
			Content: fmt.Sprintf("Nick set to: %s", nick),
		})
	} else {
		c.emit(DisplayEvent{Type: "STATUS", Content: "Nick has been cleared."})
	}
	for name, session := range c.chatKeys {
		session.nick = nick
		if nick == "" {
			session.nick = npubToTokiPona(session.pubKey)
		}
		c.chatKeys[name] = session
	}

	c.saveConfig()
	c.sendStateUpdate()
}

func (c *client) setPoW(difficultyStr string) {
	difficulty, err := strconv.Atoi(strings.TrimSpace(difficultyStr))
	if err != nil {
		c.emit(DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Invalid PoW difficulty: '%s'. Must be a number.", difficultyStr)})
		return
	}

	// Reject rather than clamp: silently storing a different target than the one
	// the user asked for is worse than telling them the target is unreachable.
	if difficulty < 0 || difficulty > maxPoW {
		c.emit(DisplayEvent{Type: "ERROR", Content: fmt.Sprintf(
			"PoW difficulty must be between 0 and %d: mining must finish inside the 10-second send deadline.", maxPoW)})
		return
	}

	activeView := c.getActiveView()
	if activeView == nil {
		c.emit(DisplayEvent{Type: "ERROR", Content: "Cannot set PoW: no active chat/group."})
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
		c.emit(DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("PoW difficulty for %s set to %d.", activeView.Name, difficulty)})
	} else {
		c.emit(DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("PoW disabled for %s.", activeView.Name)})
	}
}

// Read-only & Completions

func (c *client) listChats() {
	if len(c.config.Views) == 0 {
		c.emit(DisplayEvent{Type: "INFO", Content: "You are not in any chats. Use /join <chat_name> to join one."})
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
	c.emit(DisplayEvent{Type: "INFO", Content: builder.String()})
}

func (c *client) getActiveChat() {
	activeView := c.getActiveView()
	var content string
	if activeView != nil {
		content = fmt.Sprintf("Current active chat/group is: %s", activeView.Name)
	} else {
		content = "There is no active chat/group."
	}
	c.emit(DisplayEvent{Type: "INFO", Content: content})
}

func (c *client) getHelp() {
	helpText := "COMMANDS:\n" +
		"* /join <chat1> [chat2]... - Joins one or more chats. (Alias: /j)\n" +
		"* /set [name|names...] - Without args: shows active chat. With one name: activates a chat/group. With multiple names: creates a group. (Alias: /s)\n" +
		"* /list - Lists all your chats and groups. (Alias: /l)\n" +
		"* /del [name] - Deletes a chat/group. If no name, deletes the active chat/group. (Alias: /d)\n" +
		"* /nick [new_nick] - Sets or clears your nickname. (Alias: /n)\n" +
		"* /theme [name|number] - Lists themes or switches and saves the theme. (Alias: /t)\n" +
		"* /scrollbar [0|1|2] - Cycles silently without args; sets 0 off, 1 right (default), 2 left. Same mode shows help. (Alias: /sb)\n" +
		"* /pow [number] - Sets Proof-of-Work difficulty for the active chat/group. 0 to disable. (Alias: /p)\n" +
		"* /relay [<num>|url1...] - List, remove (#), or add anchor relays. (Alias: /r)\n" +
		"* /block [@nick] - Blocks a user. Without nick, lists blocked users. (Alias: /b)\n" +
		"* /unblock [<num>|@nick|pubkey] - Unblocks a user. Without args, lists blocked users. (Alias: /ub)\n" +
		"* /filter [word|regex|<num>] - Adds a filter. Without args, lists filters. With number, toggles off/on. (Alias: /f)\n" +
		"* /unfilter [<num>] - Removes a filter by number. Without args, clears all. (Alias: /uf)\n" +
		"* /mute [word|regex|<num>] - Adds a mute. Without args, lists mutes. With number, toggles off/on. (Alias: /m)\n" +
		"* /unmute [<num>] - Removes a mute by number. Without args, clears all. (Alias: /um)\n" +
		"* /quit - Exits the application. (Alias: /q; shortcut: Ctrl+D)"

	c.emit(DisplayEvent{Type: "INFO", Content: helpText})
}

func (c *client) handleNickCompletion(prefix string) {
	prefix = strings.TrimPrefix(prefix, "@")

	activeView := c.getActiveView()
	if activeView == nil {
		c.emit(DisplayEvent{Type: "NICK_COMPLETION_RESULT", Payload: []string{}})
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

	// Count collisions across every match before selecting the first ten.
	// A completion query must not refresh every participant's LRU recency.
	type identifier struct{ nick, suffix string }
	counts := make(map[identifier]int)
	keys := c.userContext.Keys()
	for _, key := range keys {
		value, _ := c.userContext.Peek(key)
		if _, active := relevantChats[value.chat]; active && strings.HasPrefix(value.nick, prefix) {
			counts[identifier{value.nick, value.shortPubKey}]++
		}
	}
	var entries []string
	hasCollisions := false
	for id, count := range counts {
		if count > 1 {
			hasCollisions = true
			continue
		}
		entries = addCompletion(entries, id.nick, id.suffix)
	}
	if !hasCollisions {
		c.emit(DisplayEvent{Type: "NICK_COMPLETION_RESULT", Payload: entries})
		return
	}
	// Only collisions need a second cache scan to recover full public keys.
	for _, key := range keys {
		value, _ := c.userContext.Peek(key)
		if _, active := relevantChats[value.chat]; !active || !strings.HasPrefix(value.nick, prefix) {
			continue
		}
		if counts[identifier{value.nick, value.shortPubKey}] > 1 {
			entries = addCompletion(entries, value.nick, key)
		}
	}

	c.emit(DisplayEvent{Type: "NICK_COMPLETION_RESULT", Payload: entries})
}

func addCompletion(entries []string, nick, suffix string) []string {
	if len(entries) == 10 && !completionLess(nick, suffix, entries[9]) {
		return entries
	}
	entry := "@" + nick + "#" + suffix + " "
	at := sort.SearchStrings(entries, entry)
	if len(entries) < 10 {
		entries = append(entries, "")
	}
	copy(entries[at+1:], entries[at:len(entries)-1])
	entries[at] = entry
	return entries
}

// completionLess compares the prospective entry without allocating its text.
// Comparing nicknames alone is incorrect when one contains '#' or whitespace.
func completionLess(nick, suffix, entry string) bool {
	for _, part := range [...]string{"@", nick, "#", suffix, " "} {
		n := min(len(part), len(entry))
		if cmp := strings.Compare(part[:n], entry[:n]); cmp != 0 {
			return cmp < 0
		}
		if n < len(part) {
			return false
		}
		entry = entry[n:]
	}
	return len(entry) > 0
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
		c.emit(DisplayEvent{
			Type:    "ERROR",
			Content: fmt.Sprintf("Chat or group '%s' not found.", name),
		})
		return
	}

	c.ensureViewIdentities(view)

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
		Views:           slices.Clone(c.config.Views),
		ActiveViewIndex: activeIdx,
		ChatNicks:       make(map[string]string),
	}
	for i := range state.Views {
		state.Views[i].Children = slices.Clone(state.Views[i].Children)
	}

	if len(c.config.Views) == 0 || activeIdx == -1 {
		c.emit(DisplayEvent{Type: "STATE_UPDATE", Payload: state})
		return
	}

	c.ensureViewIdentities(c.getActiveView())
	for chat, session := range c.chatKeys {
		state.ChatNicks[chat] = session.nick
	}

	c.emit(DisplayEvent{Type: "STATE_UPDATE", Payload: state})
}

func (c *client) saveConfig() {
	if err := c.config.save(); err != nil {
		log.Printf("Error saving config: %v", err)
		c.emit(DisplayEvent{
			Type:    "ERROR",
			Content: fmt.Sprintf("Failed to save configuration: %v", err),
		})
	}
}
