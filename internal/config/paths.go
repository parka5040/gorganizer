package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// DataDir returns $XDG_DATA_HOME/gorganizer.
// Falls back to ~/.local/share/gorganizer.
func DataDir() string {
	return filepath.Join(xdgDataHome(), "gorganizer")
}

// ConfigDir returns $XDG_CONFIG_HOME/gorganizer.
// Falls back to ~/.config/gorganizer.
func ConfigDir() string {
	return filepath.Join(xdgConfigHome(), "gorganizer")
}

// RuntimeDir returns $XDG_RUNTIME_DIR/gorganizer.
// Falls back to /tmp/gorganizer-<uid>.
func RuntimeDir() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "gorganizer")
	}
	return filepath.Join(os.TempDir(), "gorganizer-"+strconv.Itoa(os.Getuid()))
}

// SocketPath returns RuntimeDir()/gorganizer.sock.
func SocketPath() string {
	return filepath.Join(RuntimeDir(), "gorganizer.sock")
}

// GameModsDirName returns the folder name for a game's mods (e.g., "FalloutNV_Mods").
// Mirror of GAME_MODS_DIRS in gorganizer.sh — keep both lists in sync when
// adding a game.
var gameModsDirNames = map[string]string{
	"morrowind":  "Morrowind_Mods",
	"oblivion":   "Oblivion_Mods",
	"skyrim":     "Skyrim_Mods",
	"skyrimse":   "SkyrimSE_Mods",
	"fallout3":   "Fallout3_Mods",
	"falloutnv":  "FalloutNV_Mods",
	"fallout4":   "Fallout4_Mods",
	"starfield":  "Starfield_Mods",
}

// ModsDir returns the mods directory for a game.
// If GORGANIZER_ROOT is set (by gorganizer.sh), uses <root>/<GameName>_Mods/.
// Otherwise falls back to DataDir()/<gameID>/mods.
func ModsDir(gameID string) string {
	root := os.Getenv("GORGANIZER_ROOT")
	if root != "" {
		if name, ok := gameModsDirNames[gameID]; ok {
			return filepath.Join(root, name)
		}
		return filepath.Join(root, gameID+"_Mods")
	}
	return filepath.Join(DataDir(), gameID, "mods")
}

// AllModsDirNames returns all known game mod folder names.
func AllModsDirNames() map[string]string {
	return gameModsDirNames
}

// ProfilesDir returns DataDir()/<gameID>/profiles.
func ProfilesDir(gameID string) string {
	return filepath.Join(DataDir(), gameID, "profiles")
}

// DownloadsDir returns the per-game Downloads folder, colocated with the
// game's mods at {ModsDir}/Downloads. Initialized on first download.
func DownloadsDir(gameID string) string {
	return filepath.Join(ModsDir(gameID), "Downloads")
}

// GameSettingsPath returns the per-game settings yaml file stored at
// {ModsDir}/.gorganizer-game.yaml.
func GameSettingsPath(gameID string) string {
	return filepath.Join(ModsDir(gameID), ".gorganizer-game.yaml")
}

// EnsureDir creates a directory if it does not exist.
func EnsureDir(path string) (string, error) {
	if err := os.MkdirAll(path, 0755); err != nil {
		return "", fmt.Errorf("creating directory %s: %w", path, err)
	}
	return path, nil
}

func xdgDataHome() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share")
}

func xdgConfigHome() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config")
}
