package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// GameSettings is per-game configuration stored at {ModsDir}/.gorganizer-game.yaml.
type GameSettings struct {
	AutoInstall bool
}

func DefaultGameSettings() GameSettings {
	return GameSettings{AutoInstall: false}
}

// LoadGameSettings reads {ModsDir}/.gorganizer-game.yaml; returns defaults when missing.
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

// SaveGameSettings writes {ModsDir}/.gorganizer-game.yaml, creating the mods dir if needed.
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
