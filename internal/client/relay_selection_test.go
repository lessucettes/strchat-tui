package client

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestExhaustedGeoReserveReportsIncompleteCoverage(t *testing.T) {
	c, events := testClient(t)
	chat := "u4pruy"
	c.config.Views = []View{{Name: chat}}
	c.config.ActiveViewName = chat
	list := make([]string, 10)
	until := time.Now().Add(time.Minute)
	for i := range list {
		list[i] = fmt.Sprintf("ws://127.0.0.1:%d", 11000+i)
		c.relayRetries.Add(list[i], relayRetry{until: until, delay: time.Minute})
	}
	c.geoPlans[chat] = [][]string{list, slices.Clone(list)}
	c.updateAllSubscriptions()
	if len(c.relays) != 0 || c.nextRelayRetry.IsZero() {
		t.Fatal("exhausted reserve must wait for cooldown without opening other addresses")
	}
	var warnings int
	for len(events) > 0 {
		event := <-events
		if event.Type == "WARN" && strings.Contains(event.Content, "No relay connection") {
			warnings++
		}
	}
	if warnings != 1 {
		t.Fatalf("incomplete coverage warnings = %d, want 1", warnings)
	}
	c.updateAllSubscriptions()
	for len(events) > 0 {
		if event := <-events; event.Type == "WARN" {
			t.Fatal("unchanged coverage warning was repeated")
		}
	}
}

func TestManualRelayCooldownSurvivesViewSwitches(t *testing.T) {
	c, _ := testClient(t)
	url := "ws://127.0.0.1:12345"
	c.config.AnchorRelays = []string{url}
	c.config.Views = []View{{Name: "lobby"}, {Name: "other"}}
	until := time.Now().Add(time.Minute)
	c.relayRetries.Add(url, relayRetry{until: until, delay: time.Minute})
	for _, chat := range []string{"lobby", "other", "lobby"} {
		c.config.ActiveViewName = chat
		c.updateAllSubscriptions()
		if len(c.relays) != 0 || !c.nextRelayRetry.Equal(until) {
			t.Fatal("view switch reset cooldown or enabled an unrelated public fallback")
		}
	}
	c.config.ActiveViewName = ""
	c.config.Views = nil
	c.updateAllSubscriptions()
	if !c.nextRelayRetry.IsZero() {
		t.Fatal("inactive relays must not schedule probes")
	}
}

func TestExplicitRelaysCannotCrowdOutGeoPrimaries(t *testing.T) {
	c, _ := testClient(t)
	chat := "u4pruy"
	c.config.Views = []View{{Name: chat}}
	c.config.ActiveViewName = chat
	for i := range maxActiveRelays {
		c.config.AnchorRelays = append(c.config.AnchorRelays, fmt.Sprintf("wss://explicit-%d.example", i))
	}
	seedRelaySelection(c, []string{chat})
	c.updateAllSubscriptions()
	primary, err := closestRelays(chat, defaultRelayCount)
	if err != nil {
		t.Fatal(err)
	}
	for _, url := range primary {
		if c.relays[url] == nil {
			t.Fatalf("explicit anchors crowded out BitChat primary %s", url)
		}
	}
}
