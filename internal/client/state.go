package client

import (
	"fmt"
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
				c.emit(DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Cannot join chat. Reason: %q.", err)})
				continue outer
			}
			if utf8.RuneCountInString(normalizedName) > maxChatNameLen {
				c.emit(DisplayEvent{
					Type:    "ERROR",
					Content: fmt.Sprintf("Chat %q: Name is too long. Use at most %d characters.", normalizedName, maxChatNameLen),
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
			content = fmt.Sprintf("Chat %q: Already joined.", existingChats[0])
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
			Content: fmt.Sprintf("Cannot create group. Chats not found: %q.", strings.Join(notFoundChats, ", ")),
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
			c.emit(DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Group %q: A group with these chats already exists.", name)})
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

// activateGroupChat validates both targets before changing either selection.
func (c *client) activateGroupChat(group, chat string) {
	for i := range c.config.Views {
		view := &c.config.Views[i]
		if !view.IsGroup || view.Name != group || !slices.Contains(view.Children, chat) {
			continue
		}
		wasActive := c.config.ActiveViewName == group
		view.SelectedChat = chat
		c.setActiveView(group)
		if !wasActive {
			c.updateAllSubscriptions()
		}
		return
	}
	c.emit(DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Cannot select chat %q in group %q: the group or member does not exist.", chat, group)})
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
			c.emit(DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Group %q: Failed to save conversation selection. The current selection remains active but may not survive restart. Reason: %q.", group, err)})
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
		c.emit(DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Chat or group %q not found.", viewName)})
		return
	}

	if viewToDelete.IsGroup {
		c.deleteGroup(viewName)
		c.emit(DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("Group %q: Deleted.", viewName)})
	} else {
		c.leaveChat(viewName)
		c.emit(DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("Chat %q: Left the chat.", viewName)})
	}
}

// Settings

// setArt confirms a selection only after saving it. Re-selecting the same mode
// still emits an update so the event loop can start a new scene variation.
func (c *client) setArt(name string) {
	name = strings.TrimSpace(name)
	if !validArt(name) {
		c.emit(DisplayEvent{Type: "ERROR", Content: "Unknown ASCII scene. Use /art to list scenes."})
		return
	}
	next := *c.config
	// save normalizes view selections; never let that mutate the live config.
	next.Views = slices.Clone(next.Views)
	next.Art = name
	if err := next.save(); err != nil {
		c.emit(DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Failed to save ASCII scene. The previous setting is unchanged. Reason: %q.", err)})
		return
	}
	c.config.Art = name
	c.emit(DisplayEvent{Type: "ART_UPDATE", Content: name})
}

func (c *client) setNick(nick string) {
	nick = strings.TrimSpace(nick)
	c.config.Nick = nick

	if nick != "" {
		c.emit(DisplayEvent{
			Type:    "STATUS",
			Content: fmt.Sprintf("Nickname set to %q.", nick),
		})
	} else {
		c.emit(DisplayEvent{Type: "STATUS", Content: "Nickname cleared. Each chat now uses its session nickname."})
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
		c.emit(DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Invalid PoW difficulty %q. Enter a number.", difficultyStr)})
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
		c.emit(DisplayEvent{Type: "STATUS", Content: c.viewLogScope(activeView.Name) + fmt.Sprintf("PoW difficulty set to %d.", difficulty)})
	} else {
		c.emit(DisplayEvent{Type: "STATUS", Content: c.viewLogScope(activeView.Name) + "PoW disabled."})
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
			Content: fmt.Sprintf("Chat or group %q not found.", name),
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

	c.syncActivityPolicy()
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
		c.emit(DisplayEvent{
			Type:    "ERROR",
			Content: fmt.Sprintf("Failed to save configuration. Settings may not survive restart. Reason: %q.", err),
		})
	}
}
