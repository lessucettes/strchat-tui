package client

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestGeoReserveSkipsExistingPrimaryAndChosenReserve(t *testing.T) {
	for _, exhausted := range []bool{false, true} {
		t.Run(map[bool]string{false: "new_route", true: "exhausted"}[exhausted], func(t *testing.T) {
			c, _ := testClient(t)
			chat := "u4pruy"
			// The first family's closest reserve is already a primary of the
			// second family. It cannot replace a lost route for this chat.
			a, b, overlap := "ws://127.0.0.1:12000", "ws://127.0.0.1:12001", "ws://127.0.0.1:12002"
			x, y := "ws://127.0.0.1:12003", "ws://127.0.0.1:12004"
			common := []string{"ws://127.0.0.1:12005", "ws://127.0.0.1:12006", "ws://127.0.0.1:12007", "ws://127.0.0.1:12008"}
			first := append(append([]string{a}, common...), overlap)
			second := append([]string{b, overlap}, common[:3]...)
			if !exhausted {
				first = append(first, x)
				second = append(second, x, y)
			} else {
				second = append(second, common[3])
			}
			c.geoPlans[chat] = [][]string{first, second}
			until := time.Now().Add(time.Minute)
			for _, url := range []string{a, b} {
				c.relayRetries.Add(url, relayRetry{until: until, delay: time.Minute})
			}
			got, incomplete := c.geoReserveForChat(chat, time.Now())
			if exhausted {
				if len(got) != 0 || !incomplete {
					t.Fatalf("existing primaries falsely filled reserve: %v, incomplete=%t", got, incomplete)
				}
			} else if incomplete || !slices.Equal(got, []string{x, y}) {
				t.Fatalf("replacement routes = %v, incomplete=%t, want two distinct new routes", got, incomplete)
			}
		})
	}
}

func TestRelayCoverageNoticeIsRepeatedForAnotherView(t *testing.T) {
	c, events := testClient(t)
	url := "ws://127.0.0.1:12345"
	c.config.AnchorRelays = []string{url}
	c.config.Views = []View{{Name: "lobby"}, {Name: "other"}}
	c.relayRetries.Add(url, relayRetry{until: time.Now().Add(time.Minute), delay: time.Minute})
	for _, tc := range []struct {
		view string
		want int
	}{{"lobby", 1}, {"lobby", 0}, {"other", 1}, {"other", 0}, {"lobby", 1}} {
		c.config.ActiveViewName = tc.view
		c.updateAllSubscriptions()
		warnings := 0
		for len(events) > 0 {
			if event := <-events; event.Type == "WARN" && strings.Contains(event.Content, "No relay connection") {
				warnings++
			}
		}
		if warnings != tc.want {
			t.Fatalf("view %s: warnings=%d, want %d", tc.view, warnings, tc.want)
		}
	}
}
