package client

import "fiatjaf.com/nostr"

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
		key := nostr.Generate().Hex()
		// Parse the key and derive its public key once. The immutable signer is
		// captured by queued publishes and socket AUTH snapshots instead of
		// re-decoding and re-deriving the same secret per event.
		signer, err := newSessionSigner(key)
		if err != nil {
			continue
		}
		pubkey := signer.pubKey.Hex()
		nick := c.config.Nick
		if nick == "" {
			nick = npubToTokiPona(pubkey)
		}
		c.chatKeys[chat] = chatSession{privKey: key, pubKey: pubkey, nick: nick, signer: signer}
	}
}
