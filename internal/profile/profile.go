package profile

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/mod"
)

type Profile struct {
	Name         string    `json:"name"`
	GameID       string    `json:"game_id"`
	CreatedAt    time.Time `json:"created_at"`
	UseCustomIni bool      `json:"use_custom_ini,omitempty"`
}

type Manager struct {
	dataDir            string
	pluginLocksMu      sync.Mutex
	pluginLoadoutLocks map[string]*sync.Mutex
}

func NewManager(dataDir string) *Manager {
	return &Manager{dataDir: dataDir, pluginLoadoutLocks: make(map[string]*sync.Mutex)}
}

func (pm *Manager) loadoutLock(gameID, profileName string) *sync.Mutex {
	key := gameID + "\x00" + profileName
	pm.pluginLocksMu.Lock()
	defer pm.pluginLocksMu.Unlock()
	if pm.pluginLoadoutLocks == nil {
		pm.pluginLoadoutLocks = make(map[string]*sync.Mutex)
	}
	if lock := pm.pluginLoadoutLocks[key]; lock != nil {
		return lock
	}
	lock := &sync.Mutex{}
	pm.pluginLoadoutLocks[key] = lock
	return lock
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

const (
	pluginOrderFile = "plugin_order.txt"
	pluginStateFile = "plugin_state.txt"
)

type PluginLoadoutEntry struct {
	Filename string
	Enabled  bool
}

// LoadPluginOrder reads the saved profile plugin order.
func (pm *Manager) LoadPluginOrder(gameID, profileName string) ([]string, error) {
	lock := pm.loadoutLock(gameID, profileName)
	lock.Lock()
	defer lock.Unlock()
	return pm.loadPluginOrderUnlocked(gameID, profileName)
}

func (pm *Manager) loadPluginOrderUnlocked(gameID, profileName string) ([]string, error) {
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

// SavePluginOrder applies legacy order-only updates without losing signed activation state.
func (pm *Manager) SavePluginOrder(gameID, profileName string, filenames []string) error {
	lock := pm.loadoutLock(gameID, profileName)
	lock.Lock()
	defer lock.Unlock()

	dir := pm.ProfileDir(gameID, profileName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating profile dir %s: %w", dir, err)
	}

	stateEntries, stateExists, err := pm.loadPluginStateEntriesUnlocked(gameID, profileName)
	if err != nil {
		return err
	}
	if stateExists {
		if len(filenames) == 0 {
			return pm.savePluginLoadoutUnlocked(gameID, profileName, nil)
		}
		byName := make(map[string]PluginLoadoutEntry, len(stateEntries))
		for _, entry := range stateEntries {
			byName[strings.ToLower(entry.Filename)] = entry
		}
		reordered := make([]PluginLoadoutEntry, 0, len(filenames)+len(stateEntries))
		seen := make(map[string]struct{}, len(filenames)+len(stateEntries))
		for _, name := range filenames {
			name = strings.TrimSpace(name)
			if name == "" || strings.ContainsAny(name, "\r\n") {
				continue
			}
			key := strings.ToLower(name)
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			entry, exists := byName[key]
			if !exists {
				entry = PluginLoadoutEntry{Filename: name, Enabled: true}
			}
			reordered = append(reordered, entry)
		}
		for _, entry := range stateEntries {
			key := strings.ToLower(entry.Filename)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			reordered = append(reordered, entry)
		}
		return pm.savePluginLoadoutUnlocked(gameID, profileName, reordered)
	}

	if len(filenames) == 0 {
		path := filepath.Join(dir, pluginOrderFile)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing plugin order %s: %w", path, err)
		}
		return nil
	}
	return writePluginOrderMirror(filepath.Join(dir, pluginOrderFile), filenames)
}

// LoadPluginState reads activation overrides and reports whether plugin_state.txt exists.
func (pm *Manager) LoadPluginState(gameID, profileName string) (map[string]bool, bool, error) {
	lock := pm.loadoutLock(gameID, profileName)
	lock.Lock()
	defer lock.Unlock()
	entries, exists, err := pm.loadPluginStateEntriesUnlocked(gameID, profileName)
	if err != nil || !exists {
		return nil, exists, err
	}
	state := make(map[string]bool, len(entries))
	for _, entry := range entries {
		state[strings.ToLower(entry.Filename)] = entry.Enabled
	}
	return state, true, nil
}

func (pm *Manager) loadPluginStateEntriesUnlocked(gameID, profileName string) ([]PluginLoadoutEntry, bool, error) {
	path := filepath.Join(pm.ProfileDir(gameID, profileName), pluginStateFile)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("reading plugin state %s: %w", path, err)
	}
	defer f.Close()

	var entries []PluginLoadoutEntry
	seen := make(map[string]struct{})
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if len(line) < 2 || (line[0] != '+' && line[0] != '-') {
			return nil, true, fmt.Errorf("parsing plugin state %s: entry %q must begin with + or -", path, line)
		}
		name := strings.TrimSpace(line[1:])
		if name == "" {
			return nil, true, fmt.Errorf("parsing plugin state %s: empty plugin filename", path)
		}
		key := strings.ToLower(name)
		if _, duplicate := seen[key]; duplicate {
			return nil, true, fmt.Errorf("parsing plugin state %s: duplicate plugin %q", path, name)
		}
		seen[key] = struct{}{}
		entries = append(entries, PluginLoadoutEntry{Filename: name, Enabled: line[0] == '+'})
	}
	if err := sc.Err(); err != nil {
		return nil, true, fmt.Errorf("scanning plugin state %s: %w", path, err)
	}
	return entries, true, nil
}

// LoadPluginLoadout returns one atomic ordered activation snapshot.
func (pm *Manager) LoadPluginLoadout(gameID, profileName string) ([]PluginLoadoutEntry, error) {
	entries, _, err := pm.LoadPluginLoadoutSnapshot(gameID, profileName)
	return entries, err
}

// LoadPluginLoadoutSnapshot reports whether the snapshot came from signed state.
func (pm *Manager) LoadPluginLoadoutSnapshot(gameID, profileName string) ([]PluginLoadoutEntry, bool, error) {
	lock := pm.loadoutLock(gameID, profileName)
	lock.Lock()
	defer lock.Unlock()

	entries, exists, err := pm.loadPluginStateEntriesUnlocked(gameID, profileName)
	if err != nil {
		return nil, exists, err
	}
	if exists {
		return entries, true, nil
	}
	order, err := pm.loadPluginOrderUnlocked(gameID, profileName)
	if err != nil {
		return nil, false, err
	}
	out := make([]PluginLoadoutEntry, 0, len(order))
	for _, name := range order {
		out = append(out, PluginLoadoutEntry{Filename: name, Enabled: true})
	}
	return out, false, nil
}

// SavePluginLoadout atomically commits signed ordered activation state.
func (pm *Manager) SavePluginLoadout(gameID, profileName string, entries []PluginLoadoutEntry) error {
	lock := pm.loadoutLock(gameID, profileName)
	lock.Lock()
	defer lock.Unlock()
	return pm.savePluginLoadoutUnlocked(gameID, profileName, entries)
}

func (pm *Manager) savePluginLoadoutUnlocked(gameID, profileName string, entries []PluginLoadoutEntry) error {
	dir := pm.ProfileDir(gameID, profileName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating profile dir %s: %w", dir, err)
	}

	normalized := make([]PluginLoadoutEntry, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Filename)
		if name == "" || strings.ContainsAny(name, "\r\n") {
			continue
		}
		key := strings.ToLower(name)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, PluginLoadoutEntry{Filename: name, Enabled: entry.Enabled})
	}

	var state strings.Builder
	state.WriteString("# gorganizer authoritative ordered plugin state; + enabled, - disabled.\n")
	for _, entry := range normalized {
		if entry.Enabled {
			state.WriteByte('+')
		} else {
			state.WriteByte('-')
		}
		state.WriteString(entry.Filename)
		state.WriteByte('\n')
	}

	statePath := filepath.Join(dir, pluginStateFile)
	if err := atomicfile.WriteFile(statePath, []byte(state.String()), 0644); err != nil {
		return fmt.Errorf("writing plugin state %s: %w", statePath, err)
	}

	orderPath := filepath.Join(dir, pluginOrderFile)
	if len(normalized) == 0 {
		if err := os.Remove(orderPath); err != nil && !os.IsNotExist(err) {
			slog.Warn("plugin order compatibility mirror could not be removed",
				"path", orderPath, "err", err)
		}
		return nil
	}

	order := make([]string, 0, len(normalized))
	for _, entry := range normalized {
		order = append(order, entry.Filename)
	}
	if err := writePluginOrderMirror(orderPath, order); err != nil {
		slog.Warn("plugin order compatibility mirror could not be updated",
			"path", orderPath, "err", err)
	}
	return nil
}

func writePluginOrderMirror(path string, filenames []string) error {
	var order strings.Builder
	order.WriteString("# gorganizer compatibility plugin order — highest-priority first.\n")
	for _, name := range filenames {
		name = strings.TrimSpace(name)
		if name == "" || strings.ContainsAny(name, "\r\n") {
			continue
		}
		order.WriteString(name)
		order.WriteByte('\n')
	}
	if err := atomicfile.WriteFile(path, []byte(order.String()), 0644); err != nil {
		return fmt.Errorf("writing plugin order %s: %w", path, err)
	}
	return nil
}
