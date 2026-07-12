package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/parka/gorganizer/internal/atomicfile"
)

type Executable struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	ExePath string `json:"exe_path"`
	ToolID  string `json:"tool_id,omitempty"`
	Runner  string `json:"runner,omitempty"`

	Args          []string          `json:"args,omitempty"`
	Environment   map[string]string `json:"environment,omitempty"`
	WorkingDir    string            `json:"working_dir,omitempty"`
	PrefixAppID   int               `json:"prefix_app_id,omitempty"`
	OutputPolicy  string            `json:"output_policy,omitempty"`
	SelectedInput string            `json:"selected_input,omitempty"`

	NeedsVFSMounted    bool     `json:"needs_vfs_mounted"`
	CaptureOutputToMod string   `json:"capture_output_to_mod,omitempty"`
	SanitizeEnv        bool     `json:"sanitize_env"`
	ExtraRWPaths       []string `json:"extra_rw_paths,omitempty"`
	AutoDetected       bool     `json:"auto_detected,omitempty"`
}

type GameConfig struct {
	Name             string       `json:"name"`
	InstallPath      string       `json:"install_path"`
	DataSubpath      string       `json:"data_subpath"`
	SteamAppID       int          `json:"steam_app_id"`
	Tool             string       `json:"tool,omitempty"`
	ToolExe          string       `json:"tool_exe,omitempty"`
	ProtonPath       string       `json:"proton_path,omitempty"`
	SteamLibraryPath string       `json:"steam_library_path,omitempty"`
	LinkedFromGameID string       `json:"linked_from_game_id,omitempty"`
	Executables      []Executable `json:"executables,omitempty"`
}

type Config struct {
	Games           map[string]GameConfig `json:"games"`
	LogLevel        string                `json:"log_level"`
	NexusAPIKey     string                `json:"nexus_api_key,omitempty"`
	PreferredProton string                `json:"preferred_proton,omitempty"`
}

func DefaultConfig() *Config {
	return &Config{
		Games:    make(map[string]GameConfig),
		LogLevel: "info",
	}
}

// Load reads config.json from ConfigDir(); returns defaults when missing.
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

// Save writes config.json with 0600 perms since the API key is sensitive.
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
	if err := atomicfile.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing config %s: %w", path, err)
	}
	return nil
}

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

// EffectiveGameConfig resolves a synthetic gameID to its runtime config,
func (c *Config) EffectiveGameConfig(gameID string) (GameConfig, error) {
	gc, ok := c.Games[gameID]
	if !ok {
		return GameConfig{}, fmt.Errorf("%w: %s", ErrInvalidGameID, gameID)
	}
	if gc.LinkedFromGameID == "" {
		return gc, nil
	}
	parent, ok := c.Games[gc.LinkedFromGameID]
	if !ok {
		return GameConfig{}, fmt.Errorf("synthetic game %s links to missing parent %s",
			gameID, gc.LinkedFromGameID)
	}
	merged := gc
	merged.InstallPath = parent.InstallPath
	if merged.DataSubpath == "" {
		merged.DataSubpath = parent.DataSubpath
	}
	merged.SteamAppID = parent.SteamAppID
	if merged.SteamLibraryPath == "" {
		merged.SteamLibraryPath = parent.SteamLibraryPath
	}
	if merged.ProtonPath == "" {
		merged.ProtonPath = parent.ProtonPath
	}
	return merged, nil
}
