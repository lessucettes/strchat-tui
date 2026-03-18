package client

import (
	"strings"
)

func (c *client) setDMTarget(pubkey string) {
	pubkey = strings.TrimSpace(pubkey)

	if pubkey == "" {
		c.clearDMTarget()
		return
	}

	// Minimal validation: target must be different from our own pubkey and look like pubkey.
	// Full validation isn't possible without decoding; rely on it being found in cache for good UX.
	if pubkey == c.pk {
		c.eventsChan <- DisplayEvent{Type: "ERROR", Content: "Cannot open private chat with yourself."}
		return
	}

	targetNick := ""
	targetShort := ""
	targetChat := ""
	if ctx, ok := c.userContext.Get(pubkey); ok {
		targetNick = ctx.nick
		targetShort = ctx.shortPubKey
		targetChat = ctx.chat
	}
	// Best-effort fallback for UI.
	if targetNick == "" {
		targetNick = npubToTokiPona(pubkey)
	}
	if targetShort == "" {
		targetShort = safeSuffix(pubkey, 4)
	}

	c.dmMu.Lock()
	c.dmTargetPubKey = pubkey
	c.dmMu.Unlock()

	// Ensure the subscription scope matches the DM chat so the filtered events
	// are actually received (active view controls which chat is subscribed).
	if targetChat != "" {
		cur := c.getActiveView()
		if cur == nil || cur.Name != targetChat {
			c.setActiveView(targetChat)
			c.flushAllOrdering()
			c.updateAllSubscriptions()
		}
	}

	c.eventsChan <- DisplayEvent{
		Type:    "DM_TARGET_UPDATE",
		Payload: ChatUser{PubKey: pubkey, Nick: targetNick, ShortPubKey: targetShort, Chat: targetChat},
	}
	c.eventsChan <- DisplayEvent{
		Type:    "STATUS",
		Content: "Private chat enabled.",
	}
}

func (c *client) getDMTarget() string {
	c.dmMu.RLock()
	defer c.dmMu.RUnlock()
	return c.dmTargetPubKey
}

// isMyPubKey checks whether the given pubkey belongs to this client
// either as the main identity or one of the ephemeral chat identities.
func (c *client) isMyPubKey(pubKey string) bool {
	if pubKey == "" {
		return false
	}
	if pubKey == c.pk {
		return true
	}
	for _, s := range c.chatKeys {
		if pubKey == s.pubKey {
			return true
		}
	}
	return false
}

