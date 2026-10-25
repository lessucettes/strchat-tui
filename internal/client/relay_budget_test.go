package client

import (
	"fmt"
	"strings"
	"testing"
)

// Existing inert handles let selection run without any socket or DNS activity.
func seedRelaySelection(c *client, chats []string) {
	for _, chat := range chats {
		for _, url := range c.getRelayPoolForChat(chat) {
			c.relays[url] = &managedRelay{url: url, cancel: func() {}, updates: make(chan relaySubscription, 1)}
		}
	}
}

func TestRelayBudgetReportsIncompleteCoverage(t *testing.T) {
	c, events := testClient(t)
	t.Cleanup(func() { c.Stop(); c.wg.Wait() })
	c.config.Views = []View{{Name: "u4pruy"}}
	c.config.ActiveViewName = "u4pruy"
	for i := range maxActiveRelays {
		c.config.AnchorRelays = append(c.config.AnchorRelays, fmt.Sprintf("ws://127.0.0.1:%d", i+10000))
	}
	seedRelaySelection(c, []string{"u4pruy"})
	c.updateAllSubscriptions()
	for _, worker := range c.relays {
		if worker.done != nil {
			t.Fatal("selection-only test started a network worker")
		}
	}
	for len(events) > 0 {
		if event := <-events; event.Type == "WARN" && strings.Contains(event.Content, "coverage") && strings.Contains(event.Content, fmt.Sprint(maxActiveRelays)) {
			return
		}
	}
	t.Fatal("relay budget silently removed geographic relay coverage")
}
