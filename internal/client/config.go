package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
)

type View struct {
	Name         string   `json:"name"`
	IsGroup      bool     `json:"is_group"`
	Children     []string `json:"children"`
	PoW          int      `json:"pow,omitempty"`
	SelectedChat string   `json:"selected_chat,omitempty"`
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
	Nick           string            `json:"nick,omitempty"`
	Theme          string            `json:"theme,omitempty"`
	Scrollbar      ScrollbarPosition `json:"scrollbar"`
	Views          []View            `json:"views"`
	ActiveViewName string            `json:"active_view_name"`
	AnchorRelays   []string          `json:"anchor_relays,omitempty"`
	BlockedUsers   []blockedUser     `json:"blocked_users,omitempty"`
	Filters        []filter          `json:"filters,omitempty"`
	Mutes          []filter          `json:"mutes,omitempty"`
	path           string            `json:"-"`
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
// file is replaced by a fresh default configuration. A malformed file is
// reported as an error; error messages never include file contents.
func loadConfigFrom(path string) (*config, error) {
	conf := &config{path: path, Scrollbar: ScrollbarRight}

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
	conf.normalizeGroupSelections()

	return conf, nil
}

func (c *config) normalizeGroupSelections() {
	for i := range c.Views {
		view := &c.Views[i]
		if !view.IsGroup || len(view.Children) == 0 {
			view.SelectedChat = ""
		} else if !slices.Contains(view.Children, view.SelectedChat) {
			view.SelectedChat = view.Children[0]
		}
	}
}

// maxConfigPoW is the largest Proof-of-Work difficulty accepted in a stored
// view. It is the same bound the runtime /pow command enforces, so a config
// that loads is always one the client can actually mine.
const maxConfigPoW = maxPoW

// validate checks the configuration for malformed values. Error messages must
// not echo raw file contents.
func (c *config) validate() error {
	if !c.Scrollbar.valid() {
		return fmt.Errorf("config: scrollbar position %d is outside the supported range 0..2", c.Scrollbar)
	}
	for i, relay := range c.AnchorRelays {
		if _, err := normalizeRelayURL(relay); err != nil {
			return fmt.Errorf("config: invalid relay URL at index %d: %w", i+1, err)
		}
	}
	for _, view := range c.Views {
		if view.PoW < 0 || view.PoW > maxConfigPoW {
			return fmt.Errorf("config: view %q has pow %d outside the supported range 0..%d",
				truncateString(sanitizeString(view.Name), 32), view.PoW, maxConfigPoW)
		}
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
	c.normalizeGroupSelections()

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

// createDefaultConfig writes a fresh, secret-free default config file.
func createDefaultConfig(path string) (*config, error) {
	conf := &config{
		Theme:          DefaultTheme,
		Scrollbar:      ScrollbarRight,
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
