package client

import (
	"fiatjaf.com/nostr"
	lru "github.com/hashicorp/golang-lru/v2"
	"gitlab.com/yawning/secp256k1-voi/secec/bitcoin"
)

// authorVerifyCacheSize bounds the optional warm verification cache. Each entry
// is one 1696-byte immutable table plus cache bookkeeping; at 512 entries the
// retained heap stays below 1 MiB. Nothing is allocated until an author's first
// signature validates, so a cold client pays no memory for it.
const authorVerifyCacheSize = 512

// authorVerifyCache retains wNAF tables for authors whose signatures already
// validated. Entries are keyed by the exact typed event public key and are
// never mutated after insertion; the table itself carries an owner label
// derived inside the bitcoin package from the parsed key, so a cache hit cannot
// be redirected to a different author.
type authorVerifyCache struct {
	tables *lru.Cache[nostr.PubKey, *bitcoin.AuthorVerifyTable]
}

func newAuthorVerifyCache(size int) *authorVerifyCache {
	tables, _ := lru.New[nostr.PubKey, *bitcoin.AuthorVerifyTable](size)
	return &authorVerifyCache{tables: tables}
}

// table returns the retained table for pubkey, or nil. The caller must still
// pass the parsed key to VerifyWithTable, which re-checks the key binding.
func (c *authorVerifyCache) table(pubkey nostr.PubKey) *bitcoin.AuthorVerifyTable {
	if c == nil {
		return nil
	}
	tbl, ok := c.tables.Get(pubkey)
	if !ok {
		return nil
	}
	return tbl
}

// retain stores a table built from an already-verified key. The typed array
// key owns its bytes, so no string copy is needed; the entry is immutable after
// this call.
func (c *authorVerifyCache) retain(pubkey nostr.PubKey, key *bitcoin.SchnorrPublicKey) {
	if c == nil {
		return
	}
	c.tables.Add(pubkey, bitcoin.NewAuthorVerifyTable(key))
}

func (c *authorVerifyCache) len() int {
	if c == nil {
		return 0
	}
	return c.tables.Len()
}
