package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// GameSettings is per-game configuration stored next to the mods folder at
// {ModsDir}/.gorganizer-game.yaml. Distinct from GameConfig (which lives in
// the global config.json and tracks install paths).
type GameSettings struct {
	// AutoInstall, when true, runs the installer immediately after a download
	// completes if the archive layout is unambiguous. When false (default),
	// downloads always wait for the user to double-click in the Downloads view.
	AutoInstall bool
}

// DefaultGameSettings returns the default settings for a newly-seen game.
func DefaultGameSettings() GameSettings {
	return GameSettings{AutoInstall: false}
}

// LoadGameSettings reads {ModsDir}/.gorganizer-game.yaml. Returns defaults
// (no error) if the file does not exist.
func LoadGameSettings(gameID string) (GameSettings, error) {
	s := DefaultGameSettings()
	path := GameSettingsPath(gameID)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		switch k {
		case "auto_install":
			s.AutoInstall = (v == "true")
		}
	}
	return s, scanner.Err()
}

// SaveGameSettings writes {ModsDir}/.gorganizer-game.yaml. Creates the mods
// directory if it doesn't exist.
func SaveGameSettings(gameID string, s GameSettings) error {
	dir := ModsDir(gameID)
	if _, err := EnsureDir(dir); err != nil {
		return err
	}
	path := GameSettingsPath(gameID)

	var b strings.Builder
	b.WriteString("# Gorganizer per-game settings — auto-generated\n")
	fmt.Fprintf(&b, "auto_install: %t\n", s.AutoInstall)

	return os.WriteFile(path, []byte(b.String()), 0644)
}
