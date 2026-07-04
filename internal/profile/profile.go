package profile

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/parka/gorganizer/internal/atomicfile"
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
	if err := atomicfile.WriteFile(profilePath, profileData, 0644); err != nil {
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

	// Buffer the modlist, then write atomically so a crash can't leave a
	// truncated modlist.txt (which would silently drop the user's load order).
	var modlistBuf bytes.Buffer
	if err := mod.WriteModList(&modlistBuf, entries); err != nil {
		return fmt.Errorf("rendering modlist: %w", err)
	}
	modlistPath := filepath.Join(dir, "modlist.txt")
	if err := atomicfile.WriteFile(modlistPath, modlistBuf.Bytes(), 0644); err != nil {
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

// pluginOrderFile is the per-profile filename storing the user's plugin
// load order (one filename per line, highest-priority first). Distinct
// from plugins.txt because that gets regenerated from mod priority on
// every game launch and the engine reads it; this file is the user's
// authoritative override that the daemon reapplies during discovery.
const pluginOrderFile = "plugin_order.txt"

// LoadPluginOrder reads the saved per-profile plugin order. Returns nil
// (no override) when the file is missing.
func (pm *Manager) LoadPluginOrder(gameID, profileName string) ([]string, error) {
	path := filepath.Join(pm.ProfileDir(gameID, profileName), pluginOrderFile)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading plugin order %s: %w", path, err)
	}
	defer f.Close()

	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scanning plugin order %s: %w", path, err)
	}
	return out, nil
}

// SavePluginOrder writes the user's plugin order. Empty/nil clears the
// override (the file is removed so DiscoverPlugins falls back to
// natural-order discovery).
func (pm *Manager) SavePluginOrder(gameID, profileName string, filenames []string) error {
	dir := pm.ProfileDir(gameID, profileName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating profile dir %s: %w", dir, err)
	}
	path := filepath.Join(dir, pluginOrderFile)
	if len(filenames) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing plugin order %s: %w", path, err)
		}
		return nil
	}
	var b strings.Builder
	b.WriteString("# gorganizer plugin order — highest-priority first.\n")
	for _, name := range filenames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		b.WriteString(name)
		b.WriteByte('\n')
	}
	if err := atomicfile.WriteFile(path, []byte(b.String()), 0644); err != nil {
		return fmt.Errorf("writing plugin order %s: %w", path, err)
	}
	return nil
}
