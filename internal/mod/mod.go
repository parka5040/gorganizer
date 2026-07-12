package mod

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const ImportStagePrefix = ".gorganizer-import-"

type Mod struct {
	Name      string
	GameID    string
	BasePath  string
	Files     []string
	FileCount int
	TotalSize int64
}

func NewMod(name, gameID, basePath string) *Mod {
	return &Mod{
		Name:     name,
		GameID:   gameID,
		BasePath: basePath,
	}
}

// Scan populates Files, FileCount, and TotalSize from BasePath.
func (m *Mod) Scan() error {
	if _, err := os.Stat(m.BasePath); os.IsNotExist(err) {
		return fmt.Errorf("%w: %s", ErrNoDataDir, m.BasePath)
	}

	m.Files = nil
	m.FileCount = 0
	m.TotalSize = 0

	err := filepath.WalkDir(m.BasePath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(m.BasePath, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		m.Files = append(m.Files, rel)
		m.FileCount++
		m.TotalSize += info.Size()
		return nil
	})
	if err != nil {
		return fmt.Errorf("scanning mod %q: %w", m.Name, err)
	}
	return nil
}

// ListMods returns each subdirectory as a Mod, without scanning files.
func ListMods(modsDir, gameID string) ([]Mod, error) {
	entries, err := os.ReadDir(modsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing mods in %s: %w", modsDir, err)
	}

	var mods []Mod
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if isReservedDirName(entry.Name()) {
			continue
		}
		m := NewMod(entry.Name(), gameID, filepath.Join(modsDir, entry.Name()))
		mods = append(mods, *m)
	}
	return mods, nil
}

// isReservedDirName flags non-mod entries (Downloads, dotfiles, import staging) inside the mods dir.
func isReservedDirName(name string) bool {
	if name == "" || name[0] == '.' {
		return true
	}
	if strings.HasPrefix(name, ImportStagePrefix) {
		return true
	}
	switch name {
	case "Downloads":
		return true
	}
	return false
}
