package client

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/nbd-wtf/go-nostr"
)

type View struct {
	Name     string   `json:"name"`
	IsGroup  bool     `json:"is_group"`
	Children []string `json:"children"`
	PoW      int      `json:"pow,omitempty"`
}

type blockedUser struct {
	PubKey string `json:"pubkey"`
	Nick   string `json:"nick,omitempty"`
}

// filter defines a pattern and its current state (enabled/disabled).
type filter struct {
	Pattern string `json:"pattern"`
	Enabled bool   `json:"enabled"`
}

// config is the main structure of the configuration file.
type config struct {
	PrivateKey     string        `json:"private_key"`
	Nick           string        `json:"nick,omitempty"`
	Theme          string        `json:"theme,omitempty"`
	Views          []View        `json:"views"`
	ActiveViewName string        `json:"active_view_name"`
	AnchorRelays   []string      `json:"anchor_relays,omitempty"`
	BlockedUsers   []blockedUser `json:"blocked_users,omitempty"`
	Filters        []filter      `json:"filters,omitempty"`
	Mutes          []filter      `json:"mutes,omitempty"`
	path           string        `json:"-"`
}

// Windows uses the containing directory's ACL; Unix platforms use owner-only modes.
func privateDirMode() os.FileMode {
	return 0o700
}

func privateFileMode() os.FileMode {
	return 0o600
}

func loadConfig() (*config, error) {
	appConfigDir, err := getAppConfigDir()
	if err != nil {
		return nil, err
	}
	return loadConfigFrom(filepath.Join(appConfigDir, "config.json"))
}

// loadConfigFrom reads (or creates) the configuration file at path. A missing
// file is replaced by a freshly generated default configuration. A malformed
// file, or one carrying a private key that is not a valid nostr secret key, is
// reported as an error; error messages never include key material.
func loadConfigFrom(path string) (*config, error) {
	conf := &config{path: path}

	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return createDefaultConfig(path)
		}
		return nil, fmt.Errorf("could not open config file: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return nil, fmt.Errorf("could not read config file: %w", err)
	}
	if len(data) > 1<<20 {
		return nil, errors.New("config file exceeds 1 MiB")
	}
	if err := json.Unmarshal(data, &conf); err != nil || conf == nil {
		return nil, errors.New("could not decode config file: expected one JSON object with valid field types")
	}
	if err := conf.validate(); err != nil {
		return nil, err
	}
	// Missing (legacy) or unrecognized theme names must not prevent startup.
	if !validTheme(conf.Theme) {
		conf.Theme = DefaultTheme
	}

	return conf, nil
}

// maxConfigPoW is the largest Proof-of-Work difficulty accepted in a stored
// view. It is the same bound the runtime /pow command enforces, so a config
// that loads is always one the client can actually mine.
const maxConfigPoW = maxPoW

// validate checks the configuration for malformed values. Error messages must
// never contain the private key or any part of it.
func (c *config) validate() error {
	if err := validatePrivateKey(c.PrivateKey); err != nil {
		return err
	}
	for _, view := range c.Views {
		if view.PoW < 0 || view.PoW > maxConfigPoW {
			return fmt.Errorf("config: view %q has pow %d outside the supported range 0..%d",
				truncateString(sanitizeString(view.Name), 32), view.PoW, maxConfigPoW)
		}
	}
	return nil
}

// validatePrivateKey accepts an empty key (the client then generates an
// ephemeral identity at runtime) and otherwise requires the 64-character
// hexadecimal form used by nostr and by every config written so far.
func validatePrivateKey(key string) error {
	if key == "" {
		return nil
	}
	if len(key) != 64 {
		return fmt.Errorf("config: private_key must be a 64-character hexadecimal string (got %d characters)", len(key))
	}
	decoded, err := hex.DecodeString(key)
	if err != nil || len(decoded) != 32 {
		return fmt.Errorf("config: private_key must be a 64-character hexadecimal string")
	}
	var scalar btcec.ModNScalar
	if scalar.SetByteSlice(decoded) || scalar.IsZero() {
		return errors.New("config: private_key is outside the valid secp256k1 range")
	}
	return nil
}

// save writes the configuration back to the file. The document is validated
// first and then written to a temporary file in the same directory, flushed,
// and renamed over the target, so an interrupted or concurrent save can never
// leave a partially written config.json behind. On platforms that support it
// the file and its directory are owner-only.
func (c *config) save() error {
	if c.path == "" {
		// Refuse before touching the filesystem: a missing path must not
		// create temp files in the process working directory.
		return errors.New("config: no path set for saving")
	}
	if err := c.validate(); err != nil {
		return err
	}

	dir := filepath.Dir(c.path)
	if err := os.MkdirAll(dir, privateDirMode()); err != nil {
		return fmt.Errorf("could not create config directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".config-*.json.tmp")
	if err != nil {
		return fmt.Errorf("could not create temporary config file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		if tmpPath != "" {
			tmp.Close()
			os.Remove(tmpPath)
		}
	}()

	encoder := json.NewEncoder(tmp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(c); err != nil {
		return fmt.Errorf("could not encode config file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("could not flush config file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("could not close config file: %w", err)
	}
	if err := os.Chmod(tmpPath, privateFileMode()); err != nil {
		return fmt.Errorf("could not set config file permissions: %w", err)
	}
	if err := os.Rename(tmpPath, c.path); err != nil {
		return fmt.Errorf("could not replace config file: %w", err)
	}
	tmpPath = "" // renamed into place; nothing left to clean up

	return nil
}

// createDefaultConfig generates a new private key and a default config file.
func createDefaultConfig(path string) (*config, error) {
	sk := nostr.GeneratePrivateKey()
	conf := &config{
		PrivateKey:     sk,
		Theme:          DefaultTheme,
		Views:          []View{},
		ActiveViewName: "",
		AnchorRelays:   []string{},
		BlockedUsers:   []blockedUser{},

		Filters: []filter{},
		Mutes:   []filter{},

		path: path,
	}
	return conf, conf.save()
}

func getAppConfigDir() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("could not get user config directory: %w", err)
	}
	return filepath.Join(configDir, "strchat-tui"), nil
}
