package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

// Tests for config loading, validation and persistence hardening:
//   - the on-disk JSON shape is unchanged and backward compatible,
//   - a missing file still produces a fresh default identity,
//   - malformed files and unusable private keys fail with errors that never
//     echo key material,
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

// testSecret returns a fresh, syntactically valid nostr secret key.
func testSecret(t *testing.T) string {
	t.Helper()
	sk := nostr.GeneratePrivateKey()
	if len(sk) != 64 {
		t.Fatalf("generated secret key has %d characters, want 64", len(sk))
	}
	return sk
}

// requireNoSecretLeak fails the test if err mentions secret or a prefix of it.
func requireNoSecretLeak(t *testing.T, err error, secret string) {
	t.Helper()
	message := err.Error()
	if secret != "" && strings.Contains(message, secret) {
		t.Fatalf("error message leaks the private key: %q", message)
	}
	if len(secret) > 16 && strings.Contains(message, secret[:16]) {
		t.Fatalf("error message leaks private key material: %q", message)
	}
}

func TestLoadConfigFromCreatesPrivateDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	conf, err := loadConfigFrom(path)
	if err != nil {
		t.Fatalf("loadConfigFrom: %v", err)
	}
	if conf.path != path {
		t.Errorf("config path = %q, want %q", conf.path, path)
	}
	if len(conf.PrivateKey) != 64 {
		t.Errorf("default private key has %d characters, want 64", len(conf.PrivateKey))
	}
	if _, err := nostr.GetPublicKey(conf.PrivateKey); err != nil {
		t.Errorf("default private key is unusable: %v", err)
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

	reloaded, err := loadConfigFrom(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.PrivateKey != conf.PrivateKey {
		t.Error("default private key was not persisted")
	}
}

func TestLoadConfigFromRejectsMalformedJSONWithoutLeakingSecret(t *testing.T) {
	sk := testSecret(t)
	cases := map[string]string{
		"truncated":    `{"private_key":"` + sk + `","views":[`,
		"second_value": `{} {}`,
		"null":         `null`,
		"trailing":     `{"private_key":"` + sk + `",}`,
		"wrong_types":  `{"private_key":"` + sk + `","views":"not-a-list"}`,
		"not_json":     sk,
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
			requireNoSecretLeak(t, err, sk)
		})
	}
}

func TestLoadConfigFromRejectsInvalidPrivateKey(t *testing.T) {
	sk := testSecret(t)
	cases := map[string]string{
		"too_short": strings.Repeat("ab", 31),
		"zero":      strings.Repeat("0", 64),
		"overflow":  strings.Repeat("f", 64),
		"too_long":  sk + "00",
		"non_hex":   strings.Repeat("zz", 32),
		"padded":    " " + sk + " ",
	}
	for name, key := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			writeTestFile(t, path, `{"private_key":"`+key+`"}`, 0o600)

			_, err := loadConfigFrom(path)
			if err == nil {
				t.Fatal("expected an error for an invalid private key")
			}
			if !strings.Contains(err.Error(), "private_key") {
				t.Errorf("error should name the invalid field, got %q", err)
			}
			requireNoSecretLeak(t, err, key)
		})
	}
}

func TestLoadConfigFromAllowsMissingPrivateKey(t *testing.T) {
	cases := map[string]string{
		"empty_string": `{"private_key":""}`,
		"field_absent": `{}`,
		"other_fields": `{"nick":"alice","anchor_relays":["wss://relay.example.com"]}`,
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
			if conf.PrivateKey != "" {
				t.Errorf("private key = %q, want empty", conf.PrivateKey)
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
	sk := testSecret(t)
	original := `{
  "private_key": "` + sk + `",
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
	if conf.PrivateKey != sk {
		t.Error("private key changed on load")
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
	for _, key := range []string{"private_key", "nick", "views", "active_view_name", "anchor_relays", "blocked_users", "filters", "mutes"} {
		if _, ok := fields[key]; !ok {
			t.Errorf("saved config lost JSON key %q", key)
		}
	}
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
	conf := &config{PrivateKey: testSecret(t), Views: []View{{Name: "berlin", PoW: maxPoW + 1}}, path: path}

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

	conf := &config{PrivateKey: testSecret(t)} // path deliberately left empty
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
	conf := &config{PrivateKey: testSecret(t), Nick: "alice", path: path}

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
	if probe.Nick != "alice" || probe.PrivateKey != conf.PrivateKey {
		t.Errorf("saved config is incomplete: %+v", probe)
	}
}

func TestSaveTightensExistingFilePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeTestFile(t, path, `{"private_key":"","nick":"old"}`, 0o644)

	conf := &config{PrivateKey: testSecret(t), Nick: "new", path: path}
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
	if probe.Nick != "new" || probe.PrivateKey != conf.PrivateKey {
		t.Errorf("saved config does not match: %+v", probe)
	}
}

func TestSaveRejectsInvalidPrivateKeyWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	key := strings.Repeat("zz", 32)

	conf := &config{PrivateKey: key, path: path}
	err := conf.save()
	if err == nil {
		t.Fatal("expected save to reject an invalid private key")
	}
	requireNoSecretLeak(t, err, key)
	if _, statErr := os.Stat(path); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("config file exists after a rejected save (stat err: %v)", statErr)
	}
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

	conf := &config{PrivateKey: testSecret(t), path: path}
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
	a := &config{PrivateKey: testSecret(t), Nick: "alice", path: path}
	b := &config{PrivateKey: testSecret(t), Nick: "bob", path: path}

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
	// The nick and the key must come from the same save generation.
	if got.Nick == "alice" && got.PrivateKey != a.PrivateKey {
		t.Error("config mixes fields from different save generations")
	}
	if got.Nick == "bob" && got.PrivateKey != b.PrivateKey {
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
	if _, err := os.Stat(filepath.Join(appDir, "config.json")); err != nil {
		t.Errorf("default config not created in app config dir: %v", err)
	}
}

// BenchmarkExecutePublishMining measures the real balance the maxPoW bound is
// based on: expected hashes for a target are 2^difficulty, and mining shares the
// ten-second publish deadline with signing and acknowledgement, so a difficulty
// whose expected hashes do not fit that deadline must not be accepted.
func BenchmarkExecutePublishMining(b *testing.B) {
	key := transportTestKey
	msg := strings.Repeat("m", 200)
	hashes := uint64(0)
	for b.Loop() {
		job := publishJob{event: nostr.Event{Kind: geoChatKind, Content: msg, Tags: nostr.Tags{{"g", "u33dc0"}}},
			key: key, chat: "lobby", difficulty: 16}
		result := executePublish(context.Background(), job)
		if result.kind != "ERROR" {
			b.Fatalf("unexpected publish result: %+v", result)
		}
		// The winning nonce is the number of tries the loop actually needed.
		nonce, err := strconv.ParseUint(result.event.Tags[len(result.event.Tags)-1][1], 10, 64)
		if err != nil {
			b.Fatalf("mining left no nonce tag: %v", err)
		}
		hashes += nonce + 1
	}
	// The ten-second publish deadline has to cover 2^difficulty expected hashes;
	// this rate is what makes maxPoW reachable on ordinary hardware.
	b.ReportMetric(float64(hashes)/b.Elapsed().Seconds(), "hashes/s")
}

func BenchmarkConfigSave(b *testing.B) {
	dir := b.TempDir()
	conf := &config{PrivateKey: strings.Repeat("ab", 32), Nick: "bench", path: filepath.Join(dir, "config.json")}
	b.ReportAllocs()
	for b.Loop() {
		if err := conf.save(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkConfigLoad(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, "config.json")
	seed := &config{
		PrivateKey:     strings.Repeat("ab", 32),
		Nick:           "bench",
		Views:          []View{{Name: "berlin", Children: []string{"u33dc0"}}},
		ActiveViewName: "berlin",
		AnchorRelays:   []string{"wss://relay.example.com"},
		path:           path,
	}
	if err := seed.save(); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := loadConfigFrom(path); err != nil {
			b.Fatal(err)
		}
	}
}
