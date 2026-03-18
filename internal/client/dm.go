package client

import (
	"fmt"
	"strings"
)

// dmChatName returns a deterministic "chat" identifier for a DM between two pubkeys.
// It is symmetric: dmChatName(a,b) == dmChatName(b,a).
func dmChatName(a, b string) string {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return ""
	}
	if a > b {
		a, b = b, a
	}
	// Use short pubkey suffixes to keep the name compact.
	return fmt.Sprintf("DM-%s-%s", safeSuffix(a, 8), safeSuffix(b, 8))
}

func isDMChatName(chatName string) bool {
	return strings.HasPrefix(chatName, "DM-")
}

// setDMTarget enables DM mode.
// Payload can be either:
//   - pubkey
//   - pubkey|chatName (chatName is used to switch subscription scope even if ctx isn't available)
func (c *client) setDMTarget(pubkey string) {
	pubkey = strings.TrimSpace(pubkey)

	// If UI sends "pubkey|chat", we only care about pubkey.
	if parts := strings.SplitN(pubkey, "|", 2); len(parts) == 2 {
		pubkey = strings.TrimSpace(parts[0])
	}

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
	if ctx, ok := c.userContext.Get(pubkey); ok {
		targetNick = ctx.nick
		targetShort = ctx.shortPubKey
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

	// Create/switch to a dedicated DM chat scope so events are not mixed
	// into the currently active public chat scope.
	dmChat := dmChatName(c.pk, pubkey)
	if dmChat != "" {
		// Ensure DM view exists.
		viewExists := false
		for _, v := range c.config.Views {
			if v.Name == dmChat {
				viewExists = true
				break
			}
		}
		if !viewExists {
			c.config.Views = append(c.config.Views, View{Name: dmChat, IsGroup: false})
		}

		c.setActiveView(dmChat)
		c.flushAllOrdering()
		c.updateAllSubscriptions()
	}

	c.eventsChan <- DisplayEvent{
		Type:    "DM_TARGET_UPDATE",
		Payload: ChatUser{PubKey: pubkey, Nick: targetNick, ShortPubKey: targetShort, Chat: dmChat},
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

