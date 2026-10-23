package client

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

func TestTwentyGeoChatsKeepBothPrimaryCatalogSets(t *testing.T) {
	c, _ := testClient(t)
	var chats []string
	for _, cell := range "0123456789bcdefghjkm" {
		chats = append(chats, string(cell)+"00000")
	}
	if len(chats) != 20 {
		t.Fatalf("fixture has %d chats, want 20", len(chats))
	}
	c.config.Views = []View{{Name: "world", IsGroup: true, Children: chats}}
	c.config.ActiveViewName = "world"
	seedRelaySelection(c, chats)
	want := len(c.relays)
	if want <= 12 {
		t.Fatal("fixture must require more than the old relay budget")
	}
	c.updateAllSubscriptions()
	if len(c.relays) != want {
		t.Fatalf("selected %d primary relays, want complete union of %d", len(c.relays), want)
	}
	for _, chat := range chats {
		for _, url := range c.getRelayPoolForChat(chat) {
			if worker := c.relays[url]; worker == nil || !slices.Contains(worker.chats, chat) {
				t.Fatalf("chat %s lost its primary relay %s", chat, url)
			}
		}
	}
}

func TestGeoReserveReplacesFailedPrimaryAndReturns(t *testing.T) {
	c, _ := testClient(t)
	chat := "u4pruy"
	c.config.Views = []View{{Name: chat}}
	c.config.ActiveViewName = chat
	lists := make([][]string, 2)
	for catalog := range lists {
		for rank := range 10 {
			url := fmt.Sprintf("ws://127.0.0.1:%d", 10000+catalog*100+rank)
			lists[catalog] = append(lists[catalog], url)
			c.relays[url] = &managedRelay{url: url, cancel: func() {}, updates: make(chan relaySubscription, 1)}
		}
	}
	c.geoPlans[chat] = lists
	for _, list := range lists {
		c.relayRetries.Add(list[0], relayRetry{until: time.Now().Add(time.Minute), delay: time.Minute})
	}
	c.updateAllSubscriptions()
	for _, list := range lists {
		if c.relays[list[0]] != nil || c.relays[list[5]] == nil || c.relays[list[6]] != nil {
			t.Fatal("failed primary must use only the first reserve of its own catalog")
		}
		for _, url := range list[1:defaultRelayCount] {
			if c.relays[url] == nil {
				t.Fatalf("reserve displaced a working primary: %s", url)
			}
		}
		// Reinsert a recovered primary as an inert, stable connected handle.
		c.relayRetries.Add(list[0], relayRetry{delay: time.Minute})
		c.relays[list[0]] = &managedRelay{url: list[0], connected: true,
			connectedAt: time.Now().Add(-2 * time.Minute), chats: []string{chat},
			keys: c.subscriptionForRelay(list[0], []string{chat}).keys, cancel: func() {}}
	}
	c.updateAllSubscriptions()
	for _, list := range lists {
		if c.relays[list[0]] == nil || c.relays[list[5]] != nil {
			t.Fatal("stable recovered primary did not replace its reserve")
		}
	}
}

func TestGeoReserveCandidatesKeepBoundedPrimaryPrefixes(t *testing.T) {
	entries, err := bundledCatalogEntries()
	if err != nil {
		t.Fatal(err)
	}
	for _, chat := range []string{"u4pruy", "9q8yyk", "xn774c"} {
		t.Run(chat, func(t *testing.T) {
			lists, err := rankedGeoRelays(chat, 2*defaultRelayCount)
			if err != nil || len(lists) != len(entries) {
				t.Fatalf("ranked catalog sets: %d, error: %v", len(lists), err)
			}
			for i, list := range lists {
				primary, err := selectClosestRelays(entries[i], chat, defaultRelayCount)
				if err != nil {
					t.Fatal(err)
				}
				if len(list) != 2*defaultRelayCount || !slices.Equal(list[:defaultRelayCount], primary) {
					t.Fatalf("catalog %d changed its primary set or exceeded the bounded reserve", i)
				}
			}
		})
	}
}
