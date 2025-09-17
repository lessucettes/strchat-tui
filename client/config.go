// client/config.go
package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/nbd-wtf/go-nostr"
)

type View struct {
	Name     string   `json:"name"`
	IsGroup  bool     `json:"is_group"`
	Children []string `json:"children"`
	PoW      int      `json:"pow,omitempty"`
}

type BlockedUser struct {
	PubKey string `json:"pubkey"`
	Nick   string `json:"nick,omitempty"`
}

// Config is the main structure of the configuration file.
type Config struct {
	PrivateKey     string        `json:"private_key"`
	Nick           string        `json:"nick,omitempty"`
	Views          []View        `json:"views"`
	ActiveViewName string        `json:"active_view_name"`
	BlockedUsers   []BlockedUser `json:"blocked_users,omitempty"`

	Filters []string `json:"filters,omitempty"`
	Mutes   []string `json:"mutes,omitempty"`

	path string `json:"-"`
}

func LoadConfig() (*Config, error) {
	appConfigDir, err := GetAppConfigDir()
	if err != nil {
		return nil, err
	}

	configPath := filepath.Join(appConfigDir, "config.json")
	conf := &Config{path: configPath}

	file, err := os.Open(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return createDefaultConfig(configPath)
		}
		return nil, fmt.Errorf("could not open config file: %w", err)
	}
	defer file.Close()

	if err := json.NewDecoder(file).Decode(conf); err != nil {
		return nil, fmt.Errorf("could not decode config file: %w", err)
	}

	return conf, nil
}

// Save writes the current configuration back to the file.
func (c *Config) Save() error {
	dirPerm := os.FileMode(0755)
	filePerm := os.FileMode(0644)

	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		dirPerm = 0700
		filePerm = 0600
	}

	if err := os.MkdirAll(filepath.Dir(c.path), dirPerm); err != nil {
		return fmt.Errorf("could not create config directory: %w", err)
	}

	file, err := os.OpenFile(c.path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, filePerm)
	if err != nil {
		return fmt.Errorf("could not create config file: %w", err)
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(c); err != nil {
		return fmt.Errorf("could not encode config file: %w", err)
	}

	return nil
}

// createDefaultConfig generates a new private key and a default config file.
func createDefaultConfig(path string) (*Config, error) {
	sk := nostr.GeneratePrivateKey()
	conf := &Config{
		PrivateKey:     sk,
		Views:          []View{},
		ActiveViewName: "",
		BlockedUsers:   []BlockedUser{},
		Filters:        []string{},
		Mutes:          []string{},
		path:           path,
	}
	return conf, conf.Save()
}

func GetAppConfigDir() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("could not get user config directory: %w", err)
	}
	return filepath.Join(configDir, "strchat-tui"), nil
}
