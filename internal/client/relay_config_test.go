package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigFromRejectsInvalidAnchorWithoutRewriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	content := `{"anchor_relays":["not a url::"]}`
	writeTestFile(t, path, content, 0o600)
	if _, err := loadConfigFrom(path); err == nil || !strings.Contains(err.Error(), "relay") {
		t.Fatalf("expected invalid relay error, got %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != content {
		t.Fatal("invalid config was overwritten")
	}
}

func TestRelayRejectsMultipleNumericArguments(t *testing.T) {
	c, _ := testClient(t)
	c.config.path = filepath.Join(t.TempDir(), "config.json")
	// This command test needs no subscription; an active chat with no anchors
	// would start public default relay workers when manageAnchors refreshes it.
	c.config.Views = nil
	c.config.ActiveViewName = ""
	c.manageAnchors("1 2")
	if len(c.config.AnchorRelays) != 0 {
		t.Fatalf("numeric removal arguments stored as hostnames: %v", c.config.AnchorRelays)
	}
	if len(c.relays) != 0 {
		t.Fatal("command test unexpectedly started relay workers")
	}
}
