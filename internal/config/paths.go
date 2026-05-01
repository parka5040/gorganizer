package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

func DataDir() string {
	return filepath.Join(xdgDataHome(), "gorganizer")
}

func ConfigDir() string {
	return filepath.Join(xdgConfigHome(), "gorganizer")
}

// CacheDir returns the dir for refetchable artifacts the daemon may discard.
func CacheDir() string {
	return filepath.Join(xdgCacheHome(), "gorganizer")
}

// RuntimeDir returns $XDG_RUNTIME_DIR/gorganizer, falling back to /tmp/gorganizer-<uid>.
func RuntimeDir() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "gorganizer")
	}
	return filepath.Join(os.TempDir(), "gorganizer-"+strconv.Itoa(os.Getuid()))
}

func SocketPath() string {
	return filepath.Join(RuntimeDir(), "gorganizer.sock")
}

// LockPath returns the singleton flock file held by the running daemon.
func LockPath() string {
	return filepath.Join(RuntimeDir(), "gorganizerd.lock")
}

var gameModsDirNames = map[string]string{
	"morrowind":  "Morrowind_Mods",
	"oblivion":   "Oblivion_Mods",
	"skyrim":     "Skyrim_Mods",
	"skyrimse":   "SkyrimSE_Mods",
	"fallout3":   "Fallout3_Mods",
	"falloutnv":  "FalloutNV_Mods",
	"fallout4":   "Fallout4_Mods",
	"starfield":  "Starfield_Mods",
	"ttw":        "TTW_Mods",
}

// ModsDir returns the mods directory for a game, honoring GORGANIZER_ROOT when set.
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

func AllModsDirNames() map[string]string {
	return gameModsDirNames
}

func ProfilesDir(gameID string) string {
	return filepath.Join(DataDir(), gameID, "profiles")
}

func DownloadsDir(gameID string) string {
	return filepath.Join(ModsDir(gameID), "Downloads")
}

func GameSettingsPath(gameID string) string {
	return filepath.Join(ModsDir(gameID), ".gorganizer-game.yaml")
}

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

func xdgCacheHome() string {
	if dir := os.Getenv("XDG_CACHE_HOME"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache")
}
