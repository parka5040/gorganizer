// Package steam provides shared helpers for locating a user's Steam installation on Linux.
package steam

import (
	"fmt"
	"os"
	"path/filepath"
)

// FindRoot returns the absolute path to the user's Steam installation by walking the canonical Linux locations.
func FindRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding home directory: %w", err)
	}

	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}

	primary := filepath.Join(dataHome, "Steam")
	if dirExists(filepath.Join(primary, "steamapps")) {
		return primary, nil
	}

	symlink := filepath.Join(home, ".steam", "steam")
	if dirExists(filepath.Join(symlink, "steamapps")) {
		if resolved, err := filepath.EvalSymlinks(symlink); err == nil {
			return resolved, nil
		}
		return symlink, nil
	}

	flatpak := filepath.Join(home, ".var", "app", "com.valvesoftware.Steam", ".local", "share", "Steam")
	if dirExists(filepath.Join(flatpak, "steamapps")) {
		return flatpak, nil
	}

	return "", fmt.Errorf("steam root not found")
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
