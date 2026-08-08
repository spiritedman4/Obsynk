// Package config persists the daemon's per-vault settings: the OAuth
// client ID/secret the user entered in the plugin's settings tab, and the
// resolved Drive root folder for this vault.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Config struct {
	ClientID            string `json:"clientId"`
	ClientSecret        string `json:"clientSecret"`
	DriveRootFolderName string `json:"driveRootFolderName"`
	DriveRootFolderID   string `json:"driveRootFolderId"`
}

// Load reads path, returning a zero-value Config if the file doesn't exist
// yet (first run, before the user has connected Drive).
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Save persists cfg to path (mode 0600, since it holds an OAuth client
// secret).
func (cfg Config) Save(path string) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
