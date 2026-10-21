package client

import (
	"encoding/json"
	"os"
	"testing"
)

func TestSOCKSOptionIsRuntimeOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("APPDATA", home)
	c, err := NewWithOptions(nil, nil, Options{SOCKSProxy: "127.0.0.1:9050"})
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
}
