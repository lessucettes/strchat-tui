package main

import "testing"

// The Mage targets stamp version, commit and date through -ldflags; --version
// must stay readable when only some of them are set.
func TestVersionLine(t *testing.T) {
	savedVersion, savedCommit, savedDate := version, commit, date
	t.Cleanup(func() { version, commit, date = savedVersion, savedCommit, savedDate })

	cases := []struct {
		version, commit, date string
		want                  string
	}{
		{"dev", "", "", "dev"},
		{"v0.5.0", "", "", "v0.5.0"},
		{"v0.5.0", "abc1234", "", "v0.5.0 commit abc1234"},
		{"v0.5.0", "", "2026-09-28T19:00:00Z", "v0.5.0 built 2026-09-28T19:00:00Z"},
		{"v0.5.0", "abc1234", "2026-09-28T19:00:00Z", "v0.5.0 commit abc1234 built 2026-09-28T19:00:00Z"},
	}
	for _, tc := range cases {
		version, commit, date = tc.version, tc.commit, tc.date
		if got := versionLine(); got != tc.want {
			t.Errorf("versionLine() = %q, want %q", got, tc.want)
		}
	}
}
