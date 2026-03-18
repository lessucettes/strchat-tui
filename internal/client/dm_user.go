package client

import (
	"strings"
)

// dmUser enables DM mode for a user resolved by payload.
// Payload can be a nick prefix, a @nick#shortPubKey prefix, or a short pubkey prefix.
func (c *client) dmUser(payload string) {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		c.clearDMTarget()
		return
	}

	identifier := strings.TrimPrefix(payload, "@")
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		c.clearDMTarget()
		return
	}

	// Resolve cached user -> pubkey.
	var matchedPubKey string
	bestScore := -1

	var nickPart string
	var shortPart string
	if parts := strings.SplitN(identifier, "#", 2); len(parts) == 2 {
		nickPart = parts[0]
		shortPart = parts[1]
	}

	for _, pk := range c.userContext.Keys() {
		ctx, ok := c.userContext.Get(pk)
		if !ok {
			continue
		}

		score := -1
		if shortPart != "" || strings.Contains(identifier, "#") {
			// Match nick+short if '#' was provided.
			if nickPart != "" && !strings.HasPrefix(ctx.nick, nickPart) {
				continue
			}
			if shortPart != "" && !strings.HasPrefix(ctx.shortPubKey, shortPart) {
				continue
			}

			// Score based on match lengths.
			nLen := len(nickPart)
			sLen := len(shortPart)
			score = nLen*1000 + sLen
		} else {
			// Match by nick prefix first, then short pubkey.
			if ctx.nick != "" && strings.HasPrefix(ctx.nick, identifier) {
				score = len(identifier) * 100
			} else if ctx.shortPubKey != "" && strings.HasPrefix(ctx.shortPubKey, identifier) {
				score = len(identifier) * 10
			} else if strings.HasPrefix(pk, identifier) {
				score = len(identifier)
			}
		}

		if score > bestScore || (score == bestScore && score >= 0 && pk < matchedPubKey) {
			bestScore = score
			matchedPubKey = pk
		}
	}

	if matchedPubKey == "" {
		c.eventsChan <- DisplayEvent{
			Type:    "ERROR",
			Content: "Could not find user for /dm. Cache may be empty yet; try selecting from Users or send a message first.",
		}
		return
	}

	// Toggle off if the same user is selected.
	if c.getDMTarget() == matchedPubKey {
		c.clearDMTarget()
		return
	}

	c.setDMTarget(matchedPubKey)
}

