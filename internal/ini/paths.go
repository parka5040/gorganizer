package ini

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// SteamRoot resolves the active Steam library root.
func SteamRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}
	candidates := []string{
		filepath.Join(dataHome, "Steam"),
		filepath.Join(home, ".steam", "steam"),
		filepath.Join(home, ".var", "app", "com.valvesoftware.Steam", ".local", "share", "Steam"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(c, "steamapps")); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("Steam root not found")
}

// DocumentsPath returns the Documents/My Games/{subdir}/ dir, probing both
// "My Documents" and "Documents" inside the Proton pfx.
func DocumentsPath(steamAppID int, subdir string) (string, error) {
	if subdir == "" {
		return "", fmt.Errorf("empty my-games subdir")
	}
	if steamAppID > 0 {
		if root, err := SteamRoot(); err == nil {
			appID := strconv.Itoa(steamAppID)
			prefix := filepath.Join(
				root, "steamapps", "compatdata", appID,
				"pfx", "drive_c", "users", "steamuser",
			)
			for _, docsDir := range []string{"My Documents", "Documents"} {
				candidate := filepath.Join(prefix, docsDir, "My Games", subdir)
				if _, err := os.Stat(candidate); err == nil {
					return candidate, nil
				}
				if _, err := os.Stat(filepath.Join(prefix, docsDir)); err == nil {
					return candidate, nil
				}
			}
			return filepath.Join(prefix, "My Documents", "My Games", subdir), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Documents", "My Games", subdir), nil
}

// AppDataLocalPath returns AppData/Local/{subdir}/ inside the Proton prefix.
func AppDataLocalPath(steamAppID int, subdir string) (string, error) {
	if subdir == "" {
		return "", fmt.Errorf("empty AppData subdir")
	}
	if steamAppID > 0 {
		if root, err := SteamRoot(); err == nil {
			appID := strconv.Itoa(steamAppID)
			return filepath.Join(
				root, "steamapps", "compatdata", appID,
				"pfx", "drive_c", "users", "steamuser",
				"AppData", "Local", subdir,
			), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", subdir), nil
}
