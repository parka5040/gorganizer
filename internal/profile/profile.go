package profile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/parka/gorganizer/internal/mod"
)

// Profile represents a named configuration for a specific game.
type Profile struct {
	Name      string    `json:"name"`
	GameID    string    `json:"game_id"`
	CreatedAt time.Time `json:"created_at"`
	// UseCustomIni, when true, tells the daemon to push the profile's
	// INI files into the game's Documents/My Games/{subdir}/ directory at
	// launch time. Disabled by default — the game's own INI files are left
	// alone until the user opts in explicitly.
	UseCustomIni bool `json:"use_custom_ini,omitempty"`
}

// Manager manages profiles stored on disk.
type Manager struct {
	dataDir string // e.g., ~/.local/share/gorganizer
}

// NewManager creates a profile Manager.
func NewManager(dataDir string) *Manager {
	return &Manager{dataDir: dataDir}
}

// ProfileDir returns the filesystem path for a profile.
func (pm *Manager) ProfileDir(gameID, profileName string) string {
	return filepath.Join(pm.dataDir, gameID, "profiles", profileName)
}

// Load reads profile.json and modlist.txt for a game+profile. When the
// profile directory or profile.json doesn't exist yet, a fresh profile is
// materialized on disk and returned — the setup wizard creates the dirs
// but not the json, and the UI's "Default" placeholder would otherwise
// fail the first time anything tries to load it.
func (pm *Manager) Load(gameID, profileName string) (*Profile, []mod.ModListEntry, error) {
	dir := pm.ProfileDir(gameID, profileName)

	// Read profile.json. If it's missing, auto-create a fresh profile so
	// "Default" always works without an explicit CreateProfile call.
	profilePath := filepath.Join(dir, "profile.json")
	profileData, err := os.ReadFile(profilePath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("reading profile %s: %w", profilePath, err)
		}
		fresh := &Profile{
			Name:      profileName,
			GameID:    gameID,
			CreatedAt: time.Now(),
		}
		if err := pm.Save(fresh, nil); err != nil {
			return nil, nil, fmt.Errorf("materializing profile %s: %w", profilePath, err)
		}
		return fresh, nil, nil
	}
	var p Profile
	if err := json.Unmarshal(profileData, &p); err != nil {
		return nil, nil, fmt.Errorf("parsing profile %s: %w", profilePath, err)
	}

	// Read modlist.txt.
	modlistPath := filepath.Join(dir, "modlist.txt")
	modlistFile, err := os.Open(modlistPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &p, nil, nil
		}
		return nil, nil, fmt.Errorf("reading modlist %s: %w", modlistPath, err)
	}
	defer modlistFile.Close()

	entries, err := mod.ParseModList(modlistFile)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing modlist %s: %w", modlistPath, err)
	}

	// Drop any "Overwrite" entry from the loaded list. Overwrite is a
	// daemon-managed always-on layer (highest priority, write-capture
	// target); it's never a user-toggleable mod and must not appear in
	// modlist.txt as `+Overwrite` / `-Overwrite`. Pre-existing files may
	// still have it from older builds — silently strip on read so the rest
	// of the system never sees it as a regular entry.
	filtered := entries[:0]
	for _, e := range entries {
		if e.Name == OverwriteModName {
			continue
		}
		filtered = append(filtered, e)
	}
	entries = filtered

	return &p, entries, nil
}

// OverwriteModName is the reserved folder name for the always-on write
// capture layer. Importable into both daemon and UI code so the magic
// string "Overwrite" lives in exactly one place.
const OverwriteModName = "Overwrite"

// Save writes profile.json and modlist.txt for a profile.
func (pm *Manager) Save(p *Profile, entries []mod.ModListEntry) error {
	dir := pm.ProfileDir(p.GameID, p.Name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating profile dir %s: %w", dir, err)
	}

	// Write profile.json.
	profileData, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling profile: %w", err)
	}
	profilePath := filepath.Join(dir, "profile.json")
	if err := os.WriteFile(profilePath, profileData, 0644); err != nil {
		return fmt.Errorf("writing profile %s: %w", profilePath, err)
	}

	// Write modlist.txt — strip any Overwrite entry the caller may have
	// inadvertently passed through. Overwrite is daemon-managed and never
	// belongs in modlist.txt; persisting it would resurrect the legacy
	// `-Overwrite` line every time the user toggled an unrelated mod.
	clean := entries[:0]
	for _, e := range entries {
		if e.Name == OverwriteModName {
			continue
		}
		clean = append(clean, e)
	}
	entries = clean

	modlistPath := filepath.Join(dir, "modlist.txt")
	modlistFile, err := os.Create(modlistPath)
	if err != nil {
		return fmt.Errorf("creating modlist %s: %w", modlistPath, err)
	}
	defer modlistFile.Close()

	if err := mod.WriteModList(modlistFile, entries); err != nil {
		return fmt.Errorf("writing modlist %s: %w", modlistPath, err)
	}
	return nil
}

// List returns all profiles for a game.
func (pm *Manager) List(gameID string) ([]*Profile, error) {
	dir := filepath.Join(pm.dataDir, gameID, "profiles")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing profiles in %s: %w", dir, err)
	}

	var profiles []*Profile
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		profilePath := filepath.Join(dir, entry.Name(), "profile.json")
		data, err := os.ReadFile(profilePath)
		if err != nil {
			continue
		}
		var p Profile
		if err := json.Unmarshal(data, &p); err != nil {
			continue
		}
		profiles = append(profiles, &p)
	}
	return profiles, nil
}

// Create creates a new profile with an empty modlist.
func (pm *Manager) Create(gameID, profileName string) (*Profile, error) {
	dir := pm.ProfileDir(gameID, profileName)
	if _, err := os.Stat(dir); err == nil {
		return nil, fmt.Errorf("profile %q already exists for %s", profileName, gameID)
	}

	p := &Profile{
		Name:      profileName,
		GameID:    gameID,
		CreatedAt: time.Now(),
	}
	if err := pm.Save(p, nil); err != nil {
		return nil, err
	}
	return p, nil
}

// Delete removes a profile directory.
func (pm *Manager) Delete(gameID, profileName string) error {
	dir := pm.ProfileDir(gameID, profileName)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("deleting profile %s: %w", dir, err)
	}
	return nil
}
