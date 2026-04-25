package ini

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// SteamRoot resolves the active Steam library root. Mirrors the detection
// done in internal/tools/proton.go (kept duplicated here to avoid importing
// the tools package, which depends on ipc and config).
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

// DocumentsPath returns the Documents/My Games/{subdir}/ directory the game
// reads its INI files from. Tries the Proton compatdata pfx first, since
// Bethesda titles run through Proton on Linux; falls back to a user-profile
// Documents path for native installs.
//
// Inside the Wine prefix the documents folder is commonly named
// "My Documents" — historical Windows-style — sometimes with a "Documents"
// symlink alongside it, depending on Proton / Wine version. This function
// probes both names and returns whichever branch actually exists. When
// neither exists yet (fresh prefix, game never launched) it prefers the
// "My Documents" branch since that's what Proton's Wine fork creates.
//
// Pattern:
//   {steamRoot}/steamapps/compatdata/{appID}/pfx/drive_c/users/steamuser/
//     {My Documents|Documents}/My Games/{subdir}/
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
			// Preferred order: existing "My Documents" → existing "Documents"
			// → fall through to "My Documents" (what Wine creates on first
			// launch of a Proton prefix).
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

// AppDataLocalPath returns the AppData/Local/{subdir}/ directory inside the
// Proton prefix. This is where Bethesda engines keep plugins.txt,
// loadorder.txt, and some game-state caches — distinct from My Games which
// holds INIs. Shape:
//
//   {steamRoot}/steamapps/compatdata/{appID}/pfx/drive_c/users/steamuser/
//     AppData/Local/{subdir}/
//
// Falls back to ~/.local/share/{subdir} on native installs (rare for
// Bethesda titles).
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
