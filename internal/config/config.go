package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// GameConfig holds per-game configuration.
type GameConfig struct {
	Name        string `json:"name"`
	InstallPath string `json:"install_path"`
	DataSubpath string `json:"data_subpath"`
	SteamAppID  int    `json:"steam_app_id"`
	Tool        string `json:"tool,omitempty"`
	ToolExe     string `json:"tool_exe,omitempty"`
	ProtonPath  string `json:"proton_path,omitempty"`
}

// Config holds global daemon configuration, persisted as JSON.
type Config struct {
	Games       map[string]GameConfig `json:"games"`
	LogLevel    string                `json:"log_level"`
	NexusAPIKey string                `json:"nexus_api_key,omitempty"`
	// PreferredProton is a path to a Proton build used whenever a game
	// doesn't specify its own `proton_path`. When empty, the daemon picks
	// the top-ranked detected build (Proton 11 > 10 > 9 > Experimental > Hotfix).
	// The frontend writes this from the Settings dialog.
	PreferredProton string `json:"preferred_proton,omitempty"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Games:    make(map[string]GameConfig),
		LogLevel: "info",
	}
}

// Load reads config.json from ConfigDir(). Returns a default Config if the
// file does not exist (first boot is not an error).
func Load() (*Config, error) {
	path := filepath.Join(ConfigDir(), "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DefaultConfig(), nil
		}
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}

	cfg := DefaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}
	return cfg, nil
}

// Save writes config.json to ConfigDir() with 0600 permissions
// (API key is sensitive).
func (c *Config) Save() error {
	dir := ConfigDir()
	if _, err := EnsureDir(dir); err != nil {
		return err
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing config %s: %w", path, err)
	}
	return nil
}

// GameDataPath returns the full path to a game's Data directory.
func (c *Config) GameDataPath(gameID string) (string, error) {
	gc, ok := c.Games[gameID]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrInvalidGameID, gameID)
	}
	subpath := gc.DataSubpath
	if subpath == "" {
		subpath = "Data"
	}
	return filepath.Join(gc.InstallPath, subpath), nil
}
