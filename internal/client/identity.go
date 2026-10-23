package client

import "github.com/nbd-wtf/go-nostr"

// Chat identities belong to this run, never to a view or a configuration file.
// The owner prepares them before publishing state; previews only read them.
func (c *client) ensureViewIdentities(view *View) {
	if view == nil {
		return
	}
	chats := view.Children
	if !view.IsGroup {
		chats = []string{view.Name}
	}
	for _, chat := range chats {
		if _, exists := c.chatKeys[chat]; exists || chat == "" {
			continue
		}
		key := nostr.GeneratePrivateKey()
		pubkey, _ := nostr.GetPublicKey(key)
		nick := c.config.Nick
		if nick == "" {
			nick = npubToTokiPona(pubkey)
		}
		c.chatKeys[chat] = chatSession{privKey: key, pubKey: pubkey, nick: nick}
	}
}
