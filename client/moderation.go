// client/moderation.go
package client

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// --- Moderation Logic ---

func (c *Client) blockUser(payload string) {
	var pkToBlock, nickToBlock string

	for _, pk := range c.userContext.Keys() {
		if ctx, ok := c.userContext.Get(pk); ok {
			userIdentifier := fmt.Sprintf("@%s#%s", ctx.Nick, ctx.ShortPK)
			if strings.HasPrefix(userIdentifier, payload) {
				pkToBlock = pk
				nickToBlock = fmt.Sprintf("%s#%s", ctx.Nick, ctx.ShortPK)
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

	c.config.BlockedUsers = append(c.config.BlockedUsers, BlockedUser{PubKey: pkToBlock, Nick: nickToBlock})
	c.saveConfig()
	c.eventsChan <- DisplayEvent{Type: "STATUS", Content: fmt.Sprintf("Blocked user %s. Their messages will now be hidden.", nickToBlock)}
}

func (c *Client) unblockUser(payload string) {
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

func (c *Client) listBlockedUsers() {
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

func (c *Client) matchesAny(content string, patterns []compiledPattern) bool {
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

func (c *Client) addFilter(p string) {
	if p == "" {
		c.listFilters()
		return
	}
	c.config.Filters = append(c.config.Filters, p)
	c.saveConfig()
	c.rebuildRegexCaches()
	c.eventsChan <- DisplayEvent{Type: "STATUS", Content: "Added filter: " + p}
}

func (c *Client) listFilters() {
	if len(c.config.Filters) == 0 {
		c.eventsChan <- DisplayEvent{Type: "INFO", Content: "No filters set."}
		return
	}
	var b strings.Builder
	b.WriteString("\nFilters:")
	for i, f := range c.config.Filters {
		b.WriteString(fmt.Sprintf("\n[%d] %s", i+1, f))
	}
	c.eventsChan <- DisplayEvent{Type: "INFO", Content: b.String()}
}

func (c *Client) removeFilter(p string) {
	if p == "" {
		c.clearFilters()
		return
	}
	idx, err := strconv.Atoi(p)
	if err != nil || idx < 1 || idx > len(c.config.Filters) {
		c.eventsChan <- DisplayEvent{Type: "ERROR", Content: "Invalid filter number."}
		return
	}
	removed := c.config.Filters[idx-1]
	c.config.Filters = append(c.config.Filters[:idx-1], c.config.Filters[idx:]...)
	c.saveConfig()
	c.rebuildRegexCaches()
	c.eventsChan <- DisplayEvent{Type: "STATUS", Content: "Removed filter: " + removed}
}

func (c *Client) clearFilters() {
	c.config.Filters = []string{}
	c.saveConfig()
	c.rebuildRegexCaches()
	c.eventsChan <- DisplayEvent{Type: "STATUS", Content: "Cleared all filters."}
}

func (c *Client) addMute(p string) {
	if p == "" {
		c.listMutes()
		return
	}
	c.config.Mutes = append(c.config.Mutes, p)
	c.saveConfig()
	c.rebuildRegexCaches()
	c.eventsChan <- DisplayEvent{Type: "STATUS", Content: "Muted: " + p}
}

func (c *Client) listMutes() {
	if len(c.config.Mutes) == 0 {
		c.eventsChan <- DisplayEvent{Type: "INFO", Content: "No mutes set."}
		return
	}
	var b strings.Builder
	b.WriteString("\nMutes:")
	for i, m := range c.config.Mutes {
		b.WriteString(fmt.Sprintf("\n[%d] %s", i+1, m))
	}
	c.eventsChan <- DisplayEvent{Type: "INFO", Content: b.String()}
}

func (c *Client) removeMute(p string) {
	if p == "" {
		c.clearMutes()
		return
	}
	idx, err := strconv.Atoi(p)
	if err != nil || idx < 1 || idx > len(c.config.Mutes) {
		c.eventsChan <- DisplayEvent{Type: "ERROR", Content: "Invalid mute number."}
		return
	}
	removed := c.config.Mutes[idx-1]
	c.config.Mutes = append(c.config.Mutes[:idx-1], c.config.Mutes[idx:]...)
	c.saveConfig()
	c.rebuildRegexCaches()
	c.eventsChan <- DisplayEvent{Type: "STATUS", Content: "Removed mute: " + removed}
}

func (c *Client) clearMutes() {
	c.config.Mutes = []string{}
	c.saveConfig()
	c.rebuildRegexCaches()
	c.eventsChan <- DisplayEvent{Type: "STATUS", Content: "Cleared all mutes."}
}

func (c *Client) rebuildRegexCaches() {
	compileAll := func(src []string) []compiledPattern {
		out := make([]compiledPattern, 0, len(src))
		for _, raw := range src {
			out = append(out, compilePattern(raw))
		}
		return out
	}
	c.filtersCompiled = compileAll(c.config.Filters)
	c.mutesCompiled = compileAll(c.config.Mutes)
}
