package daemon

import (
	"log/slog"
	"path/filepath"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/ipc"
	"github.com/parka/gorganizer/internal/mod"
	"github.com/parka/gorganizer/internal/separators"
)

const trueIndexStep uint64 = 0x10

// writeTrueIndexes stamps each mod's position-in-modlist.txt into its
// metadata.yaml as `true_index:` (16-char hex, spaced by trueIndexStep).
func (d *Daemon) writeTrueIndexes(gameID string, entries []mod.ModListEntry) {
	modsDir := config.ModsDir(gameID)
	for i, e := range entries {
		modDir := filepath.Join(modsDir, e.Name)
		meta, err := download.LoadModMetadata(modDir)
		if err != nil {
			slog.Debug("writeTrueIndexes: load failed", "mod", e.Name, "err", err)
			continue
		}
		if meta.Folder == "" {
			meta.Folder = e.Name
		}
		if meta.Name == "" {
			meta.Name = e.Name
		}
		wanted := separators.FormatIndex(uint64(i+1) * trueIndexStep)
		if meta.TrueIndex == wanted {
			continue
		}
		meta.TrueIndex = wanted
		if err := download.SaveModMetadata(modDir, meta); err != nil {
			slog.Debug("writeTrueIndexes: save failed", "mod", e.Name, "err", err)
		}
	}
}

// ListSeparators returns the profile's stored separator layout. An empty
// slice is a valid response (first-run / user deleted them all).
func (d *Daemon) ListSeparators(gameID, profileName string) ([]ipc.SeparatorResult, error) {
	dir := d.profileMgr.ProfileDir(gameID, profileName)
	list, err := separators.Load(dir)
	if err != nil {
		return nil, err
	}
	out := make([]ipc.SeparatorResult, len(list))
	for i, s := range list {
		out[i] = ipc.SeparatorResult{
			Name:        s.Name,
			VisualIndex: s.VisualIndex,
			Collapsed:   s.Collapsed,
		}
	}
	return out, nil
}

func (d *Daemon) SetSeparators(gameID, profileName string, seps []ipc.SeparatorResult) error {
	dir := d.profileMgr.ProfileDir(gameID, profileName)
	out := make([]separators.Separator, len(seps))
	for i, s := range seps {
		out[i] = separators.Separator{
			Name:        s.Name,
			VisualIndex: s.VisualIndex,
			Collapsed:   s.Collapsed,
		}
	}
	return separators.Save(dir, out)
}
