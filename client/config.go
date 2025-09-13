// client/config.go
package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nbd-wtf/go-nostr"
)

type View struct {
	Name     string   `json:"name"`
	IsGroup  bool     `json:"is_group"`
	Children []string `json:"children"`
	PoW      int      `json:"pow,omitempty"`
}

// Config is the main structure of the configuration file.
type Config struct {
	PrivateKey     string `json:"private_key"`
	Nick           string `json:"nick,omitempty"`
	Views          []View `json:"views"`
	ActiveViewName string `json:"active_view_name"`

	path string `json:"-"`
}

func LoadConfig() (*Config, error) {
	// os.UserConfigDir() automatically returns the correct path for Windows, macOS, or Linux.
	configDir, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("could not get user config directory: %w", err)
	}

	appConfigDir := filepath.Join(configDir, "strchat-tui")
	configPath := filepath.Join(appConfigDir, "config.json")

	conf := &Config{path: configPath}

	file, err := os.Open(configPath)
	if err != nil {
		// If the file doesn't exist, create a default one.
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
	// Ensure the parent directory exists before writing the file.
	if err := os.MkdirAll(filepath.Dir(c.path), 0755); err != nil {
		return fmt.Errorf("could not create config directory: %w", err)
	}

	file, err := os.Create(c.path)
	if err != nil {
		return fmt.Errorf("could not create config file: %w", err)
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	// Use indentation for a human-readable JSON file.
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
		PrivateKey: sk,
		// The user starts with no channels or groups.
		Views:          []View{},
		ActiveViewName: "",
		path:           path,
	}
	// Save the newly created config to disk.
	return conf, conf.Save()
}
