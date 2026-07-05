package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/parka/gorganizer/internal/atomicfile"
)

// Executable is a user-registered Windows tool launched through the game's
// Proton prefix against the mounted VFS (xEdit, LOOT, DynDOLOD, Nemesis/Pandora,
// BodySlide, Wrye Bash, …). It is the general form of the single script-extender
// launch, which stays special-cased for its DLL-override/manifest handling.
type Executable struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	ExePath string `json:"exe_path"` // Linux path to the .exe (inside a mod, Data, or absolute)

	Args       []string `json:"args,omitempty"`        // supports %GAME_DIR%/%DATA_DIR%/%MODS_DIR%/%OVERWRITE%/%WIN:<path>%
	WorkingDir string   `json:"working_dir,omitempty"` // empty => the exe's own directory

	NeedsVFSMounted    bool     `json:"needs_vfs_mounted"`               // ensure the farm is applied+mounted first
	CaptureOutputToMod string   `json:"capture_output_to_mod,omitempty"` // target mod for new output; "" => Overwrite
	SanitizeEnv        bool     `json:"sanitize_env"`                    // strip MangoHud/overlays (parity w/ TTW/SE)
	ExtraRWPaths       []string `json:"extra_rw_paths,omitempty"`        // extra pressure-vessel RW mounts (e.g. LOD scratch)
	AutoDetected       bool     `json:"auto_detected,omitempty"`
}

// GameConfig holds per-game configuration.
type GameConfig struct {
	Name             string       `json:"name"`
	InstallPath      string       `json:"install_path"`
	DataSubpath      string       `json:"data_subpath"`
	SteamAppID       int          `json:"steam_app_id"`
	Tool             string       `json:"tool,omitempty"`
	ToolExe          string       `json:"tool_exe,omitempty"`
	ProtonPath       string       `json:"proton_path,omitempty"`
	LinkedFromGameID string       `json:"linked_from_game_id,omitempty"`
	Executables      []Executable `json:"executables,omitempty"`
}

// Config holds global daemon configuration, persisted as JSON.
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

	// 0600 because the config holds the Nexus API key. Atomic write so a crash
	// mid-save can never truncate config.json (H-13).
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
// inheriting install paths from the parent while keeping its own tooling.
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
	if merged.ProtonPath == "" {
		merged.ProtonPath = parent.ProtonPath
	}
	return merged, nil
}
