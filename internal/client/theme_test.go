package client

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSetThemePersistsAcrossReload(t *testing.T) {
	c, events := testClient(t)
	c.config.path = filepath.Join(t.TempDir(), "config.json")
	c.config.PrivateKey = testSecret(t)
	c.config.Nick = "alice"
	for _, name := range []string{"blue-gray", "red-gold", "monochrome", "default"} {
		c.handleAction(UserAction{Type: "SET_THEME", Payload: name})
		data, err := os.ReadFile(c.config.path)
		if err != nil {
			t.Fatalf("theme selection did not save configuration: %v", err)
		}
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatal(err)
		}
		var stored string
		if err := json.Unmarshal(doc["theme"], &stored); err != nil || stored != name {
			t.Fatalf("stored theme = %q, want %q (decode error: %v)", stored, name, err)
		}
		reloaded, err := loadConfigFrom(c.config.path)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(reloaded, c.config) {
			t.Fatal("theme save/load changed other settings or lost the selected theme")
		}
		found := false
		for len(events) > 0 {
			ev := <-events
			if ev.Type == "THEME_UPDATE" && ev.Content == name {
				found = true
			}
		}
		if !found {
			t.Fatal("saved theme was not sent to the UI")
		}
	}
}

func TestLoadThemeBackwardCompatibility(t *testing.T) {
	for _, tc := range []struct{ name, data, want string }{
		{"legacy", `{}`, DefaultTheme},
		{"empty", `{"theme":""}`, DefaultTheme},
		{"unknown", `{"theme":"future-theme"}`, DefaultTheme},
		{"saved", `{"theme":"red-gold"}`, "red-gold"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			writeTestFile(t, path, tc.data, 0o600)
			cfg, err := loadConfigFrom(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Theme != tc.want {
				t.Fatalf("loaded theme = %q, want %q", cfg.Theme, tc.want)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != tc.data {
				t.Fatal("loading a legacy or unknown theme rewrote the config")
			}
		})
	}
	cfg, err := loadConfigFrom(filepath.Join(t.TempDir(), "config.json"))
	if err != nil || cfg.Theme != DefaultTheme {
		t.Fatalf("new config should use default theme: %v", err)
	}
}

func TestSetThemeFailureKeepsSavedConfiguration(t *testing.T) {
	c, events := testClient(t)
	path := filepath.Join(t.TempDir(), "config.json")
	c.config.path, c.config.Theme = path, "blue-gray"
	if err := c.config.save(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"not-a-theme", "red-gold"} {
		if name == "red-gold" {
			// Replacing a directory must fail, even when tests run as root.
			c.config.path = t.TempDir()
		}
		c.handleAction(UserAction{Type: "SET_THEME", Payload: name})
		if c.config.Theme != "blue-gray" {
			t.Fatal("failed save changed the in-memory theme")
		}
		select {
		case ev := <-events:
			if ev.Type != "ERROR" || (name == "red-gold" && !strings.Contains(ev.Content, "Failed to save theme")) {
				t.Fatalf("expected visible failure, got %s", ev.Type)
			}
		default:
			t.Fatal("failed selection was not reported")
		}
		if len(events) != 0 {
			t.Fatal("failed selection emitted a success or theme update")
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("failed selection changed saved config")
		}
	}
}
