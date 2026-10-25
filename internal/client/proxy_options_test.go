package client

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestSOCKSOptionIsRuntimeOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("APPDATA", home)
	c, err := NewWithOptions(nil, nil, Options{SOCKSProxy: "127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.cancel)
	if c.relayHTTPClient == nil {
		t.Fatal("runtime proxy option was not installed")
	}
	t.Cleanup(c.relayHTTPClient.CloseIdleConnections)
	if err := c.config.save(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(c.config.path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"socks", "socks_proxy", "SOCKSProxy", "proxy"} {
		if _, ok := document[key]; ok {
			t.Fatalf("runtime proxy option leaked into configuration field %q", key)
		}
	}
	without, err := New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(without.cancel)
	if without.relayHTTPClient != nil {
		t.Fatal("proxy configuration unexpectedly survived a new launch without the flag")
	}
	for _, current := range []*client{c, without} {
		events := make(chan DisplayEvent, 32)
		actions := make(chan UserAction, 1)
		actions <- UserAction{Type: "ACTIVATE_VIEW", Payload: "lobby"}
		close(actions)
		current.eventsChan, current.actionsChan = events, actions
		current.config.Views = []View{{Name: "lobby"}}
		current.config.ActiveViewName = "lobby"
		current.config.AnchorRelays = []string{"ws://127.0.0.1:1"}
		current.Run() // Only dead loopback endpoints, including the proxy itself.
		close(events)
		count := 0
		connecting := false
		for event := range events {
			if event.Type == "STATUS" && strings.Contains(event.Content, "Connecting to relays.") {
				connecting = true
				if current == c && count == 0 {
					t.Fatal("relay connection status appeared before the proxy notice")
				}
			}
			if !strings.Contains(event.Content, "SOCKS5") {
				continue
			}
			if current == without || event.Type != "STATUS" || event.Content != "Using SOCKS5 proxy 127.0.0.1:1 for all relay connections. Direct fallback is disabled." {
				t.Fatalf("unexpected proxy startup event: %+v", event)
			}
			count++
		}
		if current == c && count != 1 {
			t.Fatalf("proxy startup notices = %d, want 1", count)
		}
		if !connecting {
			t.Fatal("fixture did not reach the relay-connecting state")
		}
	}
}
