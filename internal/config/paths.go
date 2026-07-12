package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/parka/gorganizer/internal/gamedef"
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

// ToolsDir returns the durable root for managed third-party tools.
func ToolsDir() string {
	return filepath.Join(DataDir(), "tools")
}

// ToolDataDir returns the durable root for a managed tool's mutable data.
func ToolDataDir(toolID, gameID, profileName string) string {
	return filepath.Join(DataDir(), "tools-data", toolID, gameID, profileName)
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

// ModsDir returns the mods directory for a game, honoring GORGANIZER_ROOT when set.
func ModsDir(gameID string) string {
	root := os.Getenv("GORGANIZER_ROOT")
	if root != "" {
		if g, ok := gamedef.ByID(gameID); ok && g.ModsDirName != "" {
			return filepath.Join(root, g.ModsDirName)
		}
		return filepath.Join(root, gameID+"_Mods")
	}
	return filepath.Join(DataDir(), gameID, "mods")
}

func AllModsDirNames() map[string]string {
	out := make(map[string]string, len(gamedef.All))
	for _, g := range gamedef.All {
		out[g.ID] = g.ModsDirName
	}
	return out
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
