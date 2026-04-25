package mod

import (
	"fmt"
	"os"
	"path/filepath"
)

// Mod represents a single installed mod.
// The mod folder itself contains the files that overlay the game's Data/ directory.
// There is no nested Data/ subfolder — the mod root IS the data content.
type Mod struct {
	Name      string   // directory name = display name
	GameID    string   // e.g., "skyrimse"
	BasePath  string   // absolute path to mod directory (this is the VFS layer root)
	Files     []string // relative file paths within BasePath (populated by Scan)
	FileCount int
	TotalSize int64
}

// NewMod creates a Mod.
func NewMod(name, gameID, basePath string) *Mod {
	return &Mod{
		Name:     name,
		GameID:   gameID,
		BasePath: basePath,
	}
}

// Scan walks the mod's BasePath and populates Files, FileCount, and TotalSize.
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

// ListMods reads the mods directory for a game and returns all subdirectories
// as mods, without scanning their files (deferred to Scan).
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
		// Reserved siblings that live inside {Game}_Mods/ but aren't mods.
		if isReservedDirName(entry.Name()) {
			continue
		}
		m := NewMod(entry.Name(), gameID, filepath.Join(modsDir, entry.Name()))
		mods = append(mods, *m)
	}
	return mods, nil
}

// isReservedDirName returns true for directory names that live inside the
// per-game mods directory but represent infrastructure, not installable
// mods (the Downloads library, hidden dotfiles, etc.).
func isReservedDirName(name string) bool {
	if name == "" || name[0] == '.' {
		return true
	}
	switch name {
	case "Downloads":
		return true
	}
	return false
}
