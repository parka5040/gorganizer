package daemon

import (
	"log/slog"
	"path/filepath"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
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
	p, err := ps.s.profileMgr.Create(gameID, name)
	if err != nil {
		return nil, err
	}
	return &dto.ProfileResult{
		Name:      p.Name,
		GameID:    p.GameID,
		CreatedAt: p.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}, nil
}

func (ps *ProfileService) DeleteProfile(gameID, name string) error {
	return ps.s.profileMgr.Delete(gameID, name)
}

func (ps *ProfileService) GetModList(gameID, profileName string) ([]dto.ModListEntryResult, error) {
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
	p, _, err := ps.s.profileMgr.Load(gameID, profileName)
	if err != nil {
		return err
	}

	var modEntries []mod.ModListEntry
	for _, e := range entries {
		modEntries = append(modEntries, mod.ModListEntry{
			Name:    e.ModName,
			Enabled: e.Enabled,
		})
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
			select {
			case ps.s.statusCh <- dto.StatusEventResult{VFSStatus: ps.s.svc.vfs.vfsStatus(gameID, gc, profileName, mm, modEntries)}:
			default:
			}
		}
	}
	return nil
}

// writeTrueIndexes stamps each mod's position-in-modlist.txt into its metadata.yaml.
func (ps *ProfileService) writeTrueIndexes(gameID string, entries []mod.ModListEntry) {
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

// ListSeparators returns the profile's stored separator layout plus the view state.
func (ps *ProfileService) ListSeparators(gameID, profileName string) ([]dto.SeparatorResult, bool, error) {
	dir := ps.s.profileMgr.ProfileDir(gameID, profileName)
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
	dir := ps.s.profileMgr.ProfileDir(gameID, profileName)
	out := make([]separators.Separator, len(seps))
	for i, s := range seps {
		out[i] = separators.Separator{
			Name:        s.Name,
			VisualIndex: s.VisualIndex,
			Collapsed:   s.Collapsed,
		}
	}
	return separators.SaveLayout(dir, separators.Layout{
		ViewEnabled: viewEnabled,
		Separators:  out,
	})
}
