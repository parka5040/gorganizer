package transfer

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
)

var GorganizerVersion = "dev"

const SchemaVersion = 1

const manifestEntryName = "manifest.json"

type ModEntry struct {
	Folder      string `json:"folder"`
	Name        string `json:"name"`
	FileCount   int    `json:"file_count"`
	TotalBytes  int64  `json:"total_bytes"`
	NexusModID  int    `json:"nexus_mod_id,omitempty"`
	NexusFileID int    `json:"nexus_file_id,omitempty"`
}

type Manifest struct {
	SchemaVersion        int        `json:"schema_version"`
	GorganizerVersion    string     `json:"gorganizer_version"`
	GameID               string     `json:"game_id"`
	ExportedAt           time.Time  `json:"exported_at"`
	Mods                 []ModEntry `json:"mods"`
	Profiles             []string   `json:"profiles"`
	IncludesOverwrite    bool       `json:"includes_overwrite"`
	IncludesGameSettings bool       `json:"includes_game_settings"`
}

// EncodeManifest renders a manifest as indented JSON for the archive's first entry.
func EncodeManifest(m *Manifest) ([]byte, error) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding manifest: %w", err)
	}
	return data, nil
}

// DecodeManifest parses the archive's manifest entry.
func DecodeManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("decoding manifest: %w", err)
	}
	return &m, nil
}

// buildModEntry walks one mod folder to fill counts and Nexus IDs for the manifest.
func buildModEntry(gameID, folder string) (ModEntry, error) {
	dir := filepath.Join(config.ModsDir(gameID), folder)
	entry := ModEntry{Folder: folder, Name: folder}

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		entry.FileCount++
		entry.TotalBytes += info.Size()
		return nil
	})
	if err != nil {
		return entry, fmt.Errorf("scanning mod %q: %w", folder, err)
	}

	meta, err := download.LoadModMetadata(dir)
	if err == nil && meta != nil {
		if meta.Name != "" {
			entry.Name = meta.Name
		}
		if len(meta.SourceArchives) > 0 {
			entry.NexusModID = meta.SourceArchives[0].ModID
			entry.NexusFileID = meta.SourceArchives[0].FileID
		}
	}
	return entry, nil
}

// profileExists reports whether a profile directory with a profile.json exists.
func profileExists(gameID, name string) bool {
	_, err := os.Stat(filepath.Join(config.ProfilesDir(gameID), name))
	return err == nil
}

// modFolderExists reports whether a mod folder exists under ModsDir.
func modFolderExists(gameID, folder string) bool {
	_, err := os.Stat(filepath.Join(config.ModsDir(gameID), folder))
	return err == nil
}
