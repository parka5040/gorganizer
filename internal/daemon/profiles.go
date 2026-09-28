package daemon

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/fsutil"
	"github.com/parka/gorganizer/internal/mod"
	"github.com/parka/gorganizer/internal/separators"
)

const trueIndexStep uint64 = 0x10

func (ps *ProfileService) ListProfiles(gameID string) ([]dto.ProfileResult, error) {
	profiles, err := ps.s.profileMgr.List(gameID)
	if err != nil {
		return nil, err
	}

	var results []dto.ProfileResult
	for _, p := range profiles {
		results = append(results, dto.ProfileResult{
			Name:      p.Name,
			GameID:    p.GameID,
			CreatedAt: p.CreatedAt.Format("2006-01-02T15:04:05Z"),
		})
	}
	return results, nil
}

func (ps *ProfileService) CreateProfile(gameID, name string) (*dto.ProfileResult, error) {
	if err := validateProfileName(name); err != nil {
		return nil, err
	}
	unlock := ps.s.lockProfiles(gameID)
	p, err := ps.s.profileMgr.Create(gameID, name)
	unlock()
	if err != nil {
		return nil, err
	}
	return &dto.ProfileResult{
		Name:      p.Name,
		GameID:    p.GameID,
		CreatedAt: p.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}, nil
}

// CopyProfile copies a profile while holding its game's profile mutation lock.
func (ps *ProfileService) CopyProfile(gameID, sourceName, newName string) (*dto.ProfileResult, error) {
	if err := ps.s.refuseWhenShuttingDown("copy_profile"); err != nil {
		return nil, err
	}
	if err := ps.s.awaitRecovery(); err != nil {
		return nil, err
	}
	defer ps.s.lockProfiles(gameID)()
	if err := ps.s.refuseWhenShuttingDown("copy_profile"); err != nil {
		return nil, err
	}
	p, err := ps.s.profileMgr.Copy(gameID, sourceName, newName)
	if err != nil {
		return nil, err
	}
	return &dto.ProfileResult{
		Name:      p.Name,
		GameID:    p.GameID,
		CreatedAt: p.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}, nil
}

func (ps *ProfileService) DeleteProfile(gameID, name string) error {
	if err := validateProfileName(name); err != nil {
		return err
	}
	defer ps.s.lockProfiles(gameID)()
	return ps.s.profileMgr.Delete(gameID, name)
}

func (ps *ProfileService) GetModList(gameID, profileName string) ([]dto.ModListEntryResult, error) {
	if err := validateProfileName(profileName); err != nil {
		return nil, err
	}
	_, entries, err := ps.s.profileMgr.Load(gameID, profileName)
	if err != nil {
		return nil, err
	}

	var results []dto.ModListEntryResult
	for i, e := range entries {
		results = append(results, dto.ModListEntryResult{
			ModName:  e.Name,
			Enabled:  e.Enabled,
			Priority: i,
		})
	}
	return results, nil
}

func (ps *ProfileService) SetModList(gameID, profileName string, entries []dto.ModListEntryResult) error {
	if err := validateProfileName(profileName); err != nil {
		return err
	}
	defer ps.s.lockProfiles(gameID)()
	p, current, err := ps.s.profileMgr.Load(gameID, profileName)
	if err != nil {
		return err
	}

	modEntries, err := mergeModList(config.ModsDir(gameID), entries, current)
	if err != nil {
		return err
	}
	if err := ps.s.profileMgr.Save(p, modEntries); err != nil {
		return err
	}

	ps.writeTrueIndexes(gameID, modEntries)

	ps.s.mu.RLock()
	mm, mmOk := ps.s.mountMgrs[gameID]
	ms, msOk := ps.s.mountStates[gameID]
	gc, gcOk := ps.s.config.Games[gameID]
	ps.s.mu.RUnlock()
	if mmOk && msOk && gcOk && ms.profileName == profileName && mm.IsMounted() {
		layers := ps.s.svc.vfs.buildLayers(gameID, gc, modEntries)
		if err := mm.MarkDirty(layers); err != nil {
			slog.Warn("VFS mark-dirty after modlist change failed", "game", gameID, "err", err)
		} else {
			ps.s.mu.RLock()
			status := ps.s.svc.vfs.vfsStatus(gameID, gc, profileName, mm, modEntries)
			ps.s.mu.RUnlock()
			ps.s.publishGuarded(dto.StatusEventResult{VFSStatus: status})
		}
	}
	return nil
}

// mergeModList validates the requested modlist and re-inserts each omitted current entry whose folder still exists after its nearest preceding requested neighbour.
func mergeModList(modsDir string, requested []dto.ModListEntryResult, current []mod.ModListEntry) ([]mod.ModListEntry, error) {
	named := make(map[string]bool, len(requested))
	for _, e := range requested {
		if err := download.ValidateTargetModName(e.ModName); err != nil {
			return nil, err
		}
		named[e.ModName] = true
	}
	retained := make(map[string][]mod.ModListEntry)
	seen := make(map[string]bool, len(current))
	anchor := ""
	for _, e := range current {
		if named[e.Name] {
			anchor = e.Name
			continue
		}
		if seen[e.Name] || !retainableModListEntry(modsDir, e.Name) {
			continue
		}
		seen[e.Name] = true
		retained[anchor] = append(retained[anchor], e)
	}
	merged := make([]mod.ModListEntry, 0, len(requested)+len(seen))
	merged = append(merged, retained[""]...)
	for _, e := range requested {
		merged = append(merged, mod.ModListEntry{Name: e.ModName, Enabled: e.Enabled})
		merged = append(merged, retained[e.ModName]...)
		delete(retained, e.ModName)
	}
	return merged, nil
}

// retainableModListEntry reports whether an omitted modlist entry names a valid mod whose folder still exists.
func retainableModListEntry(modsDir, name string) bool {
	return download.ValidateTargetModName(name) == nil && fsutil.DirExists(filepath.Join(modsDir, name))
}

// writeTrueIndexes stamps each mod's position-in-modlist.txt into the true_index key of its existing metadata.yaml.
func (ps *ProfileService) writeTrueIndexes(gameID string, entries []mod.ModListEntry) {
	modsDir := config.ModsDir(gameID)
	for i, e := range entries {
		if download.ValidateTargetModName(e.Name) != nil {
			continue
		}
		modDir := filepath.Join(modsDir, e.Name)
		wanted := separators.FormatIndex(uint64(i+1) * trueIndexStep)
		if _, err := os.Lstat(filepath.Join(modDir, "metadata.yaml")); errors.Is(err, fs.ErrNotExist) {
			if info, dirErr := os.Lstat(modDir); dirErr == nil && info.IsDir() {
				fresh := &download.ModMetadata{Folder: e.Name, Name: e.Name, TrueIndex: wanted, Enabled: e.Enabled}
				if err := download.SaveModMetadata(modDir, fresh); err != nil {
					slog.Debug("writeTrueIndexes: create failed", "mod", e.Name, "err", err)
				}
			}
			continue
		} else if err != nil {
			continue
		}
		meta, err := download.LoadModMetadata(modDir)
		if err != nil {
			slog.Debug("writeTrueIndexes: load failed", "mod", e.Name, "err", err)
			continue
		}
		if meta.TrueIndex == wanted {
			continue
		}
		if _, err := download.PatchModMetadataField(modDir, "true_index", wanted); err != nil {
			slog.Debug("writeTrueIndexes: patch failed", "mod", e.Name, "err", err)
		}
	}
}

// ListSeparators returns the profile's stored separator layout plus the view state.
func (ps *ProfileService) ListSeparators(gameID, profileName string) ([]dto.SeparatorResult, bool, error) {
	if err := validateProfileName(profileName); err != nil {
		return nil, false, err
	}
	dir, err := ps.s.profileMgr.CheckedProfileDir(gameID, profileName)
	if err != nil {
		return nil, false, err
	}
	layout, err := separators.LoadLayout(dir)
	if err != nil {
		return nil, false, err
	}
	out := make([]dto.SeparatorResult, len(layout.Separators))
	for i, s := range layout.Separators {
		out[i] = dto.SeparatorResult{
			Name:        s.Name,
			VisualIndex: s.VisualIndex,
			Collapsed:   s.Collapsed,
		}
	}
	return out, layout.ViewEnabled, nil
}

func (ps *ProfileService) SetSeparators(gameID, profileName string, seps []dto.SeparatorResult, viewEnabled bool) error {
	if err := validateProfileName(profileName); err != nil {
		return err
	}
	dir, err := ps.s.profileMgr.CheckedProfileDir(gameID, profileName)
	if err != nil {
		return err
	}
	out := make([]separators.Separator, len(seps))
	for i, s := range seps {
		out[i] = separators.Separator{
			Name:        s.Name,
			VisualIndex: s.VisualIndex,
			Collapsed:   s.Collapsed,
		}
	}
	defer ps.s.lockProfiles(gameID)()
	return separators.SaveLayout(dir, separators.Layout{
		ViewEnabled: viewEnabled,
		Separators:  out,
	})
}
