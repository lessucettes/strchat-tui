// client/moderation.go
package client

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// --- Moderation Logic ---

func (c *client) blockUser(payload string) {
	var pkToBlock, nickToBlock string

	for _, pk := range c.userContext.Keys() {
		if ctx, ok := c.userContext.Get(pk); ok {
			userIdentifier := fmt.Sprintf("@%s#%s", ctx.nick, ctx.shortPubKey)
			if strings.HasPrefix(userIdentifier, payload) {
				pkToBlock = pk
				nickToBlock = fmt.Sprintf("%s#%s", ctx.nick, ctx.shortPubKey)
				break
			}
		}
	}

	if pkToBlock == "" {
		c.eventsChan <- DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Could not find user matching '%s' to block.", payload)}
		return
	}

	for _, blockedUser := range c.config.BlockedUsers {
		if blockedUser.PubKey == pkToBlock {
			c.eventsChan <- DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("User %s is already blocked.", nickToBlock)}
			return
		}
	}

	c.config.BlockedUsers = append(c.config.BlockedUsers, blockedUser{PubKey: pkToBlock, Nick: nickToBlock})
	c.saveConfig()
	c.eventsChan <- DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("Blocked user %s. Their messages will now be hidden.", nickToBlock)}
}

func (c *client) unblockUser(payload string) {
	idxToRemove := -1

	if num, err := strconv.Atoi(payload); err == nil {
		if num > 0 && num <= len(c.config.BlockedUsers) {
			idxToRemove = num - 1
		} else {
			c.eventsChan <- DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Invalid number: %d. Use '/block' to see the list.", num)}
			return
		}
	} else {
		cleanPayload := strings.TrimPrefix(payload, "@")
		for i, blockedUser := range c.config.BlockedUsers {
			if strings.HasPrefix(blockedUser.Nick, cleanPayload) || strings.HasPrefix(blockedUser.PubKey, payload) {
				idxToRemove = i
				break
			}
		}
	}

	if idxToRemove == -1 {
		c.eventsChan <- DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Could not find a blocked user matching '%s'.", payload)}
		return
	}

	unblockedNick := c.config.BlockedUsers[idxToRemove].Nick
	if unblockedNick == "" {
		unblockedNick = c.config.BlockedUsers[idxToRemove].PubKey[:8] + "..."
	}

	c.config.BlockedUsers = append(c.config.BlockedUsers[:idxToRemove], c.config.BlockedUsers[idxToRemove+1:]...)
	c.saveConfig()
	c.eventsChan <- DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("Unblocked user %s.", unblockedNick)}
}

func (c *client) listBlockedUsers() {
	if len(c.config.BlockedUsers) == 0 {
		c.eventsChan <- DisplayEvent{Type: "INFO", Content: "Your block list is empty. Use /block <@nick> to block someone."}
		return
	}

	var builder strings.Builder
	builder.WriteString("Blocked Users:\n")
	for i, user := range c.config.BlockedUsers {
		nick := user.Nick
		if nick == "" {
			nick = "(no nick saved)"
		}
		builder.WriteString(fmt.Sprintf("[%d] - %s (%s...)\n", i+1, nick, user.PubKey[:8]))
	}
	c.eventsChan <- DisplayEvent{Type: "INFO", Content: builder.String()}
}

func compilePattern(p string) compiledPattern {
	p = strings.TrimSpace(p)
	if len(p) > 1 && strings.HasPrefix(p, "/") && strings.HasSuffix(p, "/") {
		body := p[1 : len(p)-1]
		if re, err := regexp.Compile(body); err == nil {
			return compiledPattern{raw: p, regex: re}
		}
		return compiledPattern{raw: p, literal: body}
	}
	return compiledPattern{raw: p, literal: p}
}

func (c *client) matchesAny(content string, patterns []compiledPattern) bool {
	for _, pat := range patterns {
		if pat.regex != nil {
			if pat.regex.MatchString(content) {
				return true
			}
		} else if pat.literal != "" {
			if strings.Contains(content, pat.literal) {
				return true
			}
		}
	}
	return false
}

// --- Filter Management ---

func (c *client) handleFilter(payload string) {
	if payload == "" {
		c.listFilters()
		return
	}

	if idx, err := strconv.Atoi(payload); err == nil {
		c.toggleFilter(idx)
		return
	}

	c.addFilter(payload)
}

func (c *client) addFilter(p string) {
	newFilter := filter{Pattern: p, Enabled: true}
	c.config.Filters = append(c.config.Filters, newFilter)
	c.saveConfig()
	c.rebuildRegexCaches()
	c.eventsChan <- DisplayEvent{Type: "STATUS", Content: "Added and enabled filter: " + p}
}

func (c *client) toggleFilter(idx int) {
	if idx < 1 || idx > len(c.config.Filters) {
		c.eventsChan <- DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Invalid filter number: %d. Use '/filter' to see the list.", idx)}
		return
	}
	filterIndex := idx - 1

	c.config.Filters[filterIndex].Enabled = !c.config.Filters[filterIndex].Enabled

	c.saveConfig()
	c.rebuildRegexCaches()

	status := "disabled"
	if c.config.Filters[filterIndex].Enabled {
		status = "enabled"
	}
	c.eventsChan <- DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("Filter %d (%s) is now %s.", idx, c.config.Filters[filterIndex].Pattern, status)}
}

func (c *client) listFilters() {
	if len(c.config.Filters) == 0 {
		c.eventsChan <- DisplayEvent{Type: "INFO", Content: "No filters set."}
		return
	}
	var b strings.Builder
	b.WriteString("\nFilters:")
	for i, f := range c.config.Filters {
		var statusSymbol string
		if f.Enabled {
			statusSymbol = "+"
		} else {
			statusSymbol = "-"
		}
		b.WriteString(fmt.Sprintf("\n[%d] %s %s", i+1, statusSymbol, f.Pattern))
	}
	c.eventsChan <- DisplayEvent{Type: "INFO", Content: b.String()}
}

func (c *client) removeFilter(p string) {
	if p == "" {
		c.clearFilters()
		return
	}
	idx, err := strconv.Atoi(p)
	if err != nil || idx < 1 || idx > len(c.config.Filters) {
		c.eventsChan <- DisplayEvent{Type: "ERROR", Content: "Invalid filter number."}
		return
	}
	removed := c.config.Filters[idx-1].Pattern
	c.config.Filters = append(c.config.Filters[:idx-1], c.config.Filters[idx:]...)
	c.saveConfig()
	c.rebuildRegexCaches()
	c.eventsChan <- DisplayEvent{Type: "STATUS", Content: "Removed filter: " + removed}
}

func (c *client) clearFilters() {
	c.config.Filters = []filter{}
	c.saveConfig()
	c.rebuildRegexCaches()
	c.eventsChan <- DisplayEvent{Type: "STATUS", Content: "Cleared all filters."}
}

// --- Mute Management ---

func (c *client) handleMute(payload string) {
	if payload == "" {
		c.listMutes()
		return
	}
	if idx, err := strconv.Atoi(payload); err == nil {
		c.toggleMute(idx)
		return
	}
	c.addMute(payload)
}

func (c *client) addMute(p string) {
	newMute := filter{Pattern: p, Enabled: true}
	c.config.Mutes = append(c.config.Mutes, newMute)
	c.saveConfig()
	c.rebuildRegexCaches()
	c.eventsChan <- DisplayEvent{Type: "STATUS", Content: "Muted and enabled: " + p}
}

func (c *client) toggleMute(idx int) {
	if idx < 1 || idx > len(c.config.Mutes) {
		c.eventsChan <- DisplayEvent{Type: "ERROR", Content: fmt.Sprintf("Invalid mute number: %d. Use '/mute' to see the list.", idx)}
		return
	}
	muteIndex := idx - 1

	c.config.Mutes[muteIndex].Enabled = !c.config.Mutes[muteIndex].Enabled
	c.saveConfig()
	c.rebuildRegexCaches()

	status := "disabled"
	if c.config.Mutes[muteIndex].Enabled {
		status = "enabled"
	}
	c.eventsChan <- DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("Mute %d (%s) is now %s.", idx, c.config.Mutes[muteIndex].Pattern, status)}
}

func (c *client) listMutes() {
	if len(c.config.Mutes) == 0 {
		c.eventsChan <- DisplayEvent{Type: "INFO", Content: "No mutes set."}
		return
	}
	var b strings.Builder
	b.WriteString("\nMutes:")
	for i, m := range c.config.Mutes {
		var statusSymbol string
		if m.Enabled {
			statusSymbol = "+"
		} else {
			statusSymbol = "-"
		}
		b.WriteString(fmt.Sprintf("\n[%d] %s %s", i+1, statusSymbol, m.Pattern))
	}
	c.eventsChan <- DisplayEvent{Type: "INFO", Content: b.String()}
}

func (c *client) removeMute(p string) {
	if p == "" {
		c.clearMutes()
		return
	}
	idx, err := strconv.Atoi(p)
	if err != nil || idx < 1 || idx > len(c.config.Mutes) {
		c.eventsChan <- DisplayEvent{Type: "ERROR", Content: "Invalid mute number."}
		return
	}
	removed := c.config.Mutes[idx-1].Pattern
	c.config.Mutes = append(c.config.Mutes[:idx-1], c.config.Mutes[idx:]...)
	c.saveConfig()
	c.rebuildRegexCaches()
	c.eventsChan <- DisplayEvent{Type: "STATUS", Content: "Removed mute: " + removed}
}

func (c *client) clearMutes() {
	c.config.Mutes = []filter{}
	c.saveConfig()
	c.rebuildRegexCaches()
	c.eventsChan <- DisplayEvent{Type: "STATUS", Content: "Cleared all mutes."}
}

func (c *client) rebuildRegexCaches() {
	compileAll := func(src []filter) []compiledPattern {
		out := make([]compiledPattern, 0, len(src))
		for _, item := range src {
			if item.Enabled {
				out = append(out, compilePattern(item.Pattern))
			}
		}
		return out
	}
	c.filtersCompiled = compileAll(c.config.Filters)
	c.mutesCompiled = compileAll(c.config.Mutes)
}
