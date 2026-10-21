package client

import "testing"

// Keep the README's Go regexp example tied to the actual receive path.
func TestMuteLatinRegexp(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		muted         bool
	}{
		{"ASCII", "hello", true},
		{"mixed", "Привет, Alice", true},
		{"accented", "café mañana", true},
		{"URL", "https://example.org", true},
		{"multiline", "Привет\nhello", true},
		{"Cyrillic", "Привет, мир!", false},
		{"numbers", "12345", false},
		{"Greek", "γειά", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, events := testClient(t)
			c.config.Mutes = []filter{{Pattern: `/\p{Latin}/`, Enabled: true}}
			c.rebuildRegexCaches()
			if c.mutesCompiled[0].regex == nil {
				t.Fatal("README example was not compiled as Go regexp")
			}
			c.processEvent(signedEvent(t, tc.content), "local")
			displayed := false
			for len(events) > 0 {
				if (<-events).Type == "NEW_MESSAGE" {
					displayed = true
				}
			}
			if displayed == tc.muted {
				t.Fatalf("displayed = %t, muted = %t", displayed, tc.muted)
			}
		})
	}
}
