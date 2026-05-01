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
	Name         string    `json:"name"`
	GameID       string    `json:"game_id"`
	CreatedAt    time.Time `json:"created_at"`
	UseCustomIni bool      `json:"use_custom_ini,omitempty"`
}

// Manager manages profiles stored on disk.
type Manager struct {
	dataDir string
}

func NewManager(dataDir string) *Manager {
	return &Manager{dataDir: dataDir}
}

func (pm *Manager) ProfileDir(gameID, profileName string) string {
	return filepath.Join(pm.dataDir, gameID, "profiles", profileName)
}

// Load reads profile.json and modlist.txt, auto-creating a fresh profile when missing.
func (pm *Manager) Load(gameID, profileName string) (*Profile, []mod.ModListEntry, error) {
	dir := pm.ProfileDir(gameID, profileName)

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

// OverwriteModName is the reserved folder name for the always-on write capture layer.
const OverwriteModName = "Overwrite"

// Save writes profile.json and modlist.txt for a profile.
func (pm *Manager) Save(p *Profile, entries []mod.ModListEntry) error {
	dir := pm.ProfileDir(p.GameID, p.Name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating profile dir %s: %w", dir, err)
	}

	profileData, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling profile: %w", err)
	}
	profilePath := filepath.Join(dir, "profile.json")
	if err := os.WriteFile(profilePath, profileData, 0644); err != nil {
		return fmt.Errorf("writing profile %s: %w", profilePath, err)
	}

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

func (pm *Manager) Delete(gameID, profileName string) error {
	dir := pm.ProfileDir(gameID, profileName)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("deleting profile %s: %w", dir, err)
	}
	return nil
}
