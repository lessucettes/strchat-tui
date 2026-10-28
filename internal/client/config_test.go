package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// Tests for config loading, validation and persistence hardening:
//   - the on-disk JSON holds no private key or other identity/secret field,
//   - a missing file produces a fresh secret-free default,
//   - malformed files fail with decode errors that never echo file contents,
//   - saves are atomic (temp file + rename) and owner-only where the platform
//     supports it.

// ownerOnlyModes reports whether config.save must restrict permissions, i.e.
// the same platforms config.save itself special-cases.
func ownerOnlyModes() bool { return runtime.GOOS == "linux" || runtime.GOOS == "darwin" }

// writeTestFile writes content to path with an explicit mode, bypassing the
// process umask so permission assertions are exact.
func writeTestFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

// dirEntryNames lists the entries of dir in sorted order.
func dirEntryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// assertNoSecretFields fails when raw config JSON carries an identity or
// secret-bearing field. The client keeps no persistent key, so no credential
// may ever be written to disk.
func assertNoSecretFields(t *testing.T, raw []byte) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("config is not a JSON object: %v", err)
	}
	for _, key := range []string{"private_key", "secret_key", "nsec"} {
		if _, ok := fields[key]; ok {
			t.Errorf("config contains secret field %q: %s", key, raw)
		}
	}
}

// requireNoContentEcho fails the test if err repeats raw file content.
func requireNoContentEcho(t *testing.T, err error, marker string) {
	t.Helper()
	if strings.Contains(err.Error(), marker) {
		t.Fatalf("error message echoes file content: %q", err.Error())
	}
}

func TestConfigSaveFailureIsReportedOnce(t *testing.T) {
	c, events := testClient(t)
	c.config.path = t.TempDir() // A file cannot replace this directory.
	var captured bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&captured)
	defer log.SetOutput(previous)
	c.saveConfig()
	select {
	case event := <-events:
		if event.Type != "ERROR" || !strings.Contains(event.Content, "Failed to save configuration") {
			t.Fatalf("save failure was not reported clearly: %+v", event)
		}
	default:
		t.Fatal("save failure was not reported")
	}
	if captured.Len() != 0 || len(events) != 0 {
		t.Fatal("save failure was also written to the standard logger or reported twice")
	}
}

func TestArtPreferencePersistsAndRejectsFailedChanges(t *testing.T) {
	c, events := testClient(t)
	path := filepath.Join(t.TempDir(), "config.json")
	c.config.path, c.config.Nick = path, "alice"
	c.config.Theme = DefaultTheme
	for _, name := range []string{"windmill", "rain", "rain", "off", "random"} {
		c.handleAction(UserAction{Type: "SET_ART", Payload: name})
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("art selection did not save configuration: %v", err)
		}
		var saved struct{ Art string }
		if err := json.Unmarshal(raw, &saved); err != nil || saved.Art != name {
			t.Fatalf("stored art = %q, want %q (decode error: %v)", saved.Art, name, err)
		}
		reloaded, err := loadConfigFrom(path)
		if err != nil || !reflect.DeepEqual(c.config, reloaded) {
			t.Fatalf("art preference or other settings did not survive reload: %v", err)
		}
		c.config = reloaded
		select {
		case ev := <-events:
			if ev.Type != "ART_UPDATE" || ev.Content != name {
				t.Fatalf("saved art not sent to the UI: %+v", ev)
			}
		default:
			t.Fatal("saved art was not acknowledged")
		}
		if len(events) != 0 {
			t.Fatal("art selection emitted unexpected extra events")
		}
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []UserAction{
		{Type: "SET_ART", Payload: "not-a-scene"},
		{Type: "SET_ART", Payload: "windmill"},
		{Type: "SET_THEME", Payload: "red-gold"},
		{Type: "SET_SCROLLBAR", Payload: "2"},
	} {
		t.Run(action.Type+"/"+action.Payload, func(t *testing.T) {
			// Exercise normalization through each appearance save without letting
			// a shallow expected snapshot hide changes to the original views.
			c.config.Views = []View{{Name: "lobby"}, {Name: "coffee"}, {
				Name: "mixed", IsGroup: true, Children: []string{"lobby", "coffee"}, SelectedChat: "removed",
			}}
			if action.Payload != "not-a-scene" {
				c.config.path = t.TempDir() // Atomic replacement of a directory must fail.
			}
			unchanged, err := json.Marshal(c.config)
			if err != nil {
				t.Fatal(err)
			}
			c.handleAction(action)
			current, err := json.Marshal(c.config)
			if err != nil || !bytes.Equal(current, unchanged) {
				t.Fatal("failed selection changed live settings or group destination")
			}
			select {
			case ev := <-events:
				if ev.Type != "ERROR" {
					t.Fatalf("selection failure was not reported: %+v", ev)
				}
			default:
				t.Fatal("selection failure was silent")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) || len(events) != 0 {
				t.Fatal("failed selection changed saved settings or acknowledged success")
			}
		})
	}
}

func TestLoadArtPreferenceDefaultsAndCompatibility(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`{}`, "random"}, {`{"art":""}`, "random"}, {`{"art":"future-scene"}`, "random"},
		{`{"art":"windmill"}`, "windmill"}, {`{"art":"random"}`, "random"}, {`{"art":"off"}`, "off"},
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		writeTestFile(t, path, tc.raw, 0o600)
		cfg, err := loadConfigFrom(path)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Art != tc.want {
			t.Fatalf("loaded art = %q, want %q for %s", cfg.Art, tc.want, tc.raw)
		}
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != tc.raw {
			t.Fatal("loading an art preference rewrote configuration")
		}
	}
}

func TestLoadConfigFromCreatesSecretFreeDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	conf, err := loadConfigFrom(path)
	if err != nil {
		t.Fatalf("loadConfigFrom: %v", err)
	}
	if conf.path != path {
		t.Errorf("config path = %q, want %q", conf.path, path)
	}
	if conf.Theme != DefaultTheme {
		t.Errorf("default theme = %q, want %q", conf.Theme, DefaultTheme)
	}
	if conf.Art != DefaultArt {
		t.Errorf("default art = %q, want %q", conf.Art, DefaultArt)
	}
	if conf.Scrollbar != ScrollbarRight {
		t.Errorf("default scrollbar = %v, want right", conf.Scrollbar)
	}
	if conf.Nick != "" || len(conf.Views) != 0 || conf.ActiveViewName != "" {
		t.Errorf("default config is not empty: %+v", conf)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("default config was not written: %v", err)
	}
	if ownerOnlyModes() && info.Mode().Perm() != 0o600 {
		t.Errorf("default config mode = %o, want 600", info.Mode().Perm())
	}

	if names := dirEntryNames(t, dir); len(names) != 1 || names[0] != "config.json" {
		t.Errorf("config directory contains %v, want only config.json", names)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read default config: %v", err)
	}
	assertNoSecretFields(t, raw)

	reloaded, err := loadConfigFrom(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.path != conf.path || reloaded.Nick != "" || reloaded.Theme != DefaultTheme ||
		reloaded.ActiveViewName != "" || len(reloaded.Views) != 0 || len(reloaded.AnchorRelays) != 0 {
		t.Errorf("default config did not round trip: %+v", reloaded)
	}
}

func TestLoadConfigFromRejectsMalformedJSONWithoutEchoingContent(t *testing.T) {
	marker := "SENSITIVE-MARKER"
	cases := map[string]string{
		"truncated":    `{"nick":"` + marker + `","views":[`,
		"second_value": `{} {}`,
		"null":         `null`,
		"trailing":     `{"nick":"` + marker + `",}`,
		"wrong_types":  `{"nick":"` + marker + `","views":"not-a-list"}`,
		"not_json":     marker,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			writeTestFile(t, path, content, 0o600)

			_, err := loadConfigFrom(path)
			if err == nil {
				t.Fatal("expected an error for a malformed config")
			}
			if !strings.Contains(err.Error(), "decode") {
				t.Errorf("error should identify the decode failure, got %q", err)
			}
			requireNoContentEcho(t, err, marker)
		})
	}
}

func TestLoadConfigFromKeepsExistingFileUntouched(t *testing.T) {
	cases := map[string]string{
		"empty_object":  `{}`,
		"settings_only": `{"nick":"alice","anchor_relays":["wss://relay.example.com"]}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			writeTestFile(t, path, content, 0o600)

			conf, err := loadConfigFrom(path)
			if err != nil {
				t.Fatalf("loadConfigFrom: %v", err)
			}
			if conf.path != path {
				t.Errorf("config path = %q, want %q", conf.path, path)
			}
			// Loading must not rewrite an existing file.
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read config: %v", err)
			}
			if string(raw) != content {
				t.Errorf("loadConfigFrom rewrote the file: %q", raw)
			}
		})
	}
}

func TestLoadConfigFromPreservesExistingShape(t *testing.T) {
	original := `{
  "nick": "alice",
  "views": [
    {"name": "berlin", "is_group": false, "children": ["u33dc0"], "pow": 9},
    {"name": "group-one", "is_group": true, "children": ["u33dc0", "9q8yy"]}
  ],
  "active_view_name": "group-one",
  "anchor_relays": ["wss://relay.example.com", "wss://anchor.example.net"],
  "blocked_users": [{"pubkey": "` + strings.Repeat("ab", 32) + `", "nick": "mallory"}, {"pubkey": "` + strings.Repeat("cd", 32) + `"}],
  "filters": [{"pattern": "spam", "enabled": true}, {"pattern": "noise", "enabled": false}],
  "mutes": [{"pattern": "crypto", "enabled": false}]
}`
	path := filepath.Join(t.TempDir(), "config.json")
	writeTestFile(t, path, original, 0o600)

	conf, err := loadConfigFrom(path)
	if err != nil {
		t.Fatalf("loadConfigFrom: %v", err)
	}
	if conf.Nick != "alice" || conf.ActiveViewName != "group-one" {
		t.Errorf("scalar fields changed: nick=%q active_view=%q", conf.Nick, conf.ActiveViewName)
	}
	if len(conf.Views) != 2 || conf.Views[0].Name != "berlin" || conf.Views[0].IsGroup || conf.Views[0].PoW != 9 {
		t.Errorf("views decoded incorrectly: %+v", conf.Views)
	}
	if len(conf.Views[1].Children) != 2 || !conf.Views[1].IsGroup {
		t.Errorf("group view decoded incorrectly: %+v", conf.Views[1])
	}
	if !reflect.DeepEqual(conf.AnchorRelays, []string{"wss://relay.example.com", "wss://anchor.example.net"}) {
		t.Errorf("anchor relays decoded incorrectly: %v", conf.AnchorRelays)
	}
	if len(conf.BlockedUsers) != 2 || conf.BlockedUsers[0].Nick != "mallory" || conf.BlockedUsers[1].Nick != "" {
		t.Errorf("blocked users decoded incorrectly: %+v", conf.BlockedUsers)
	}
	if len(conf.Filters) != 2 || !conf.Filters[0].Enabled || conf.Filters[1].Enabled {
		t.Errorf("filters decoded incorrectly: %+v", conf.Filters)
	}
	if len(conf.Mutes) != 1 || conf.Mutes[0].Enabled {
		t.Errorf("mutes decoded incorrectly: %+v", conf.Mutes)
	}

	if err := conf.save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	reloaded, err := loadConfigFrom(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !reflect.DeepEqual(conf, reloaded) {
		t.Errorf("save/load round trip changed the config:\nbefore: %+v\nafter:  %+v", conf, reloaded)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("saved config is not valid JSON: %v", err)
	}
	for _, key := range []string{"nick", "views", "active_view_name", "anchor_relays", "blocked_users", "filters", "mutes"} {
		if _, ok := fields[key]; !ok {
			t.Errorf("saved config lost JSON key %q", key)
		}
	}
	assertNoSecretFields(t, raw)
}

func TestLoadConfigFromRejectsOutOfRangePoW(t *testing.T) {
	cases := map[string]string{
		"pow_above_mineable_bound": `{"views":[{"name":"berlin","children":["u33dc0"],"pow":` + fmt.Sprint(maxPoW+1) + `}]}`,
		"pow_negative":             `{"views":[{"name":"berlin","children":["u33dc0"],"pow":-1}]}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			writeTestFile(t, path, content, 0o600)

			_, err := loadConfigFrom(path)
			if err == nil {
				t.Fatal("expected an error for an out-of-range PoW difficulty")
			}
			if !strings.Contains(err.Error(), "pow") {
				t.Errorf("error should mention the pow field, got %q", err)
			}
		})
	}
}

func TestLoadConfigFromAcceptsBoundedPoW(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeTestFile(t, path, fmt.Sprintf(`{"views":[{"name":"a","children":[],"pow":0},{"name":"b","children":[],"pow":%d}]}`, maxPoW), 0o600)

	conf, err := loadConfigFrom(path)
	if err != nil {
		t.Fatalf("loadConfigFrom: %v", err)
	}
	if len(conf.Views) != 2 || conf.Views[0].PoW != 0 || conf.Views[1].PoW != maxPoW {
		t.Errorf("pow values decoded incorrectly: %+v", conf.Views)
	}
}

func TestSaveRejectsOutOfRangePoW(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	conf := &config{Views: []View{{Name: "berlin", PoW: maxPoW + 1}}, path: path}

	err := conf.save()
	if err == nil {
		t.Fatal("expected save to reject an out-of-range PoW difficulty")
	}
	if !strings.Contains(err.Error(), "pow") {
		t.Errorf("error should mention the pow field, got %q", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("config file exists after a rejected save (stat err: %v)", statErr)
	}
}

func TestSaveRejectsInvalidRelayWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	conf := &config{AnchorRelays: []string{"http://relay.example.com"}, path: path}

	err := conf.save()
	if err == nil {
		t.Fatal("expected save to reject an invalid relay URL")
	}
	if !strings.Contains(err.Error(), "relay") {
		t.Errorf("error should mention the relay field, got %q", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("config file exists after a rejected save (stat err: %v)", statErr)
	}
}

func TestSaveWithEmptyPathCreatesNothing(t *testing.T) {
	work := t.TempDir()
	t.Cleanup(func() { _ = os.Chmod(work, 0o700) })
	t.Chdir(work)
	// A read-only working directory makes any temp-file creation attempt fail
	// with an OS permission error, so a missing-path guard (asserted below)
	// must be what fails first.
	if err := os.Chmod(work, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	conf := &config{Nick: "alice"} // path deliberately left empty
	err := conf.save()
	if err == nil {
		t.Fatal("expected save to fail without a config path")
	}
	if !strings.Contains(err.Error(), "path") {
		t.Errorf("expected a missing-path error before any filesystem write, got %q", err)
	}
	if names := dirEntryNames(t, work); len(names) != 0 {
		t.Errorf("save without a path created %v in the working directory", names)
	}
}

func TestSaveIsPrivateAndLeavesNoTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deeper", "config.json")
	conf := &config{Nick: "alice", path: path}

	if err := conf.save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat saved config: %v", err)
	}
	if ownerOnlyModes() {
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("config mode = %o, want 600", got)
		}
		for _, d := range []string{filepath.Dir(path), filepath.Dir(filepath.Dir(path))} {
			dirInfo, err := os.Stat(d)
			if err != nil {
				t.Fatalf("stat config dir %s: %v", d, err)
			}
			if got := dirInfo.Mode().Perm(); got != 0o700 {
				t.Errorf("config dir %s mode = %o, want 700", d, got)
			}
		}
	}

	var strays []string
	if err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && p != path {
			strays = append(strays, p)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk config dir: %v", err)
	}
	if len(strays) != 0 {
		t.Errorf("temporary files left behind: %v", strays)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	var probe config
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("saved config is not complete JSON: %v", err)
	}
	if probe.Nick != "alice" {
		t.Errorf("saved config is incomplete: %+v", probe)
	}
	assertNoSecretFields(t, raw)
}

func TestSaveTightensExistingFilePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeTestFile(t, path, `{"nick":"old"}`, 0o644)

	conf := &config{Nick: "new", path: path}
	if err := conf.save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	if ownerOnlyModes() {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("config mode = %o, want 600", got)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(raw), "old") {
		t.Errorf("previous config content survived the save: %q", raw)
	}
	var probe config
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("saved config is not valid JSON: %v", err)
	}
	if probe.Nick != "new" {
		t.Errorf("saved config does not match: %+v", probe)
	}
	assertNoSecretFields(t, raw)
}

func TestSaveRemovesTemporaryFileWhenRenameFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	// A non-empty directory at the target path makes the final rename fail.
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	marker := filepath.Join(path, "marker")
	writeTestFile(t, marker, "x", 0o600)

	conf := &config{Nick: "alice", path: path}
	if err := conf.save(); err == nil {
		t.Fatal("expected save to fail when the target cannot be replaced")
	}

	if names := dirEntryNames(t, dir); len(names) != 1 || names[0] != "config.json" {
		t.Errorf("temporary file left behind: %v", names)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("existing target was damaged: %v", err)
	}
}

func TestConcurrentSavesLeaveOneValidGeneration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	a := &config{Nick: "alice", AnchorRelays: []string{"wss://a.example.com"}, path: path}
	b := &config{Nick: "bob", AnchorRelays: []string{"wss://b.example.com"}, path: path}

	var wg sync.WaitGroup
	for _, conf := range []*config{a, b} {
		for range 16 {
			wg.Add(1)
			go func(c *config) {
				defer wg.Done()
				if err := c.save(); err != nil {
					t.Errorf("save: %v", err)
				}
			}(conf)
		}
	}
	wg.Wait()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var got config
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("config is not valid JSON after concurrent saves: %v\n%s", err, raw)
	}
	if got.Nick != "alice" && got.Nick != "bob" {
		t.Errorf("nick = %q, want one of alice/bob", got.Nick)
	}
	// The nick and the relay set must come from the same save generation.
	if got.Nick == "alice" && !reflect.DeepEqual(got.AnchorRelays, a.AnchorRelays) {
		t.Error("config mixes fields from different save generations")
	}
	if got.Nick == "bob" && !reflect.DeepEqual(got.AnchorRelays, b.AnchorRelays) {
		t.Error("config mixes fields from different save generations")
	}
	if names := dirEntryNames(t, dir); len(names) != 1 || names[0] != "config.json" {
		t.Errorf("temporary files left behind: %v", names)
	}
}

func TestGetAppConfigDirHonorsXDG(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG_CONFIG_HOME redirection is only used on linux")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	appDir, err := getAppConfigDir()
	if err != nil {
		t.Fatalf("getAppConfigDir: %v", err)
	}
	if want := filepath.Join(dir, "strchat-tui"); appDir != want {
		t.Errorf("getAppConfigDir = %q, want %q", appDir, want)
	}

	conf, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if filepath.Dir(conf.path) != appDir {
		t.Errorf("config path %q is not inside %q", conf.path, appDir)
	}
	raw, err := os.ReadFile(filepath.Join(appDir, "config.json"))
	if err != nil {
		t.Fatalf("default config not created in app config dir: %v", err)
	}
	assertNoSecretFields(t, raw)
}
