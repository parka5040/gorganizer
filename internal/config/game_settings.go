package config

import (
	"fmt"
	"os"

	"github.com/parka/gorganizer/internal/kvfile"
)

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

	sc := kvfile.NewScanner(f)
	for sc.Scan() {
		k, v, ok := kvfile.CutKV(sc.Line().Text)
		if !ok {
			continue
		}
		switch k {
		case "auto_install":
			s.AutoInstall = (kvfile.TrimValue(v) == "true")
		}
	}
	return s, sc.Err()
}

// SaveGameSettings writes {ModsDir}/.gorganizer-game.yaml, creating the mods dir if needed.
func SaveGameSettings(gameID string, s GameSettings) error {
	dir := ModsDir(gameID)
	if _, err := EnsureDir(dir); err != nil {
		return err
	}
	path := GameSettingsPath(gameID)

	var w kvfile.Writer
	w.Comment("Gorganizer per-game settings — auto-generated")
	w.KVBool("auto_install", s.AutoInstall)
	return w.WriteAtomic(path, 0644)
}
