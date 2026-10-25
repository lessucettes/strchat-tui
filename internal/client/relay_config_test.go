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
