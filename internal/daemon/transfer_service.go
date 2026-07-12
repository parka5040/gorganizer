package daemon

import (
	"context"
	"fmt"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/transfer"
)

// ExportInstance streams selected mods/profiles into an archive, holding each mod's install lock while it is read.
func (ts *TransferService) ExportInstance(ctx context.Context, req dto.ExportRequest, emit func(dto.TransferProgress)) (dto.TransferSummary, error) {
	if err := ts.validGame(req.GameID); err != nil {
		return dto.TransferSummary{}, err
	}
	opts := transfer.ExportOptions{
		GameID:              req.GameID,
		OutputPath:          req.OutputPath,
		ModFolders:          req.ModFolders,
		ProfileNames:        req.ProfileNames,
		IncludeOverwrite:    req.IncludeOverwrite,
		IncludeGameSettings: req.IncludeGameSettings,
		LockMod: func(name string) func() {
			return ts.s.lockMods(req.GameID, name)
		},
	}
	return transfer.Export(ctx, opts, emit)
}

// PreviewImport reads only an archive's manifest and reports collisions against the target instance.
func (ts *TransferService) PreviewImport(gameID, archivePath string) (dto.ImportPreview, error) {
	if err := ts.validGame(gameID); err != nil {
		return dto.ImportPreview{}, err
	}
	return transfer.Preview(gameID, archivePath)
}

// ImportInstance applies an archive under the collision policy, refusing overwrites of mounted state.
func (ts *TransferService) ImportInstance(ctx context.Context, req dto.ImportRequest, emit func(dto.TransferProgress)) (dto.TransferSummary, error) {
	if err := ts.validGame(req.GameID); err != nil {
		return dto.TransferSummary{}, err
	}
	preview, err := transfer.Preview(req.GameID, req.ArchivePath)
	if err != nil {
		return dto.TransferSummary{}, err
	}
	if err := ts.refuseMountedOverwrites(req, preview); err != nil {
		return dto.TransferSummary{}, err
	}
	opts := transfer.ImportOptions{
		GameID:             req.GameID,
		ArchivePath:        req.ArchivePath,
		Policy:             req.Policy,
		ModPolicyOverrides: req.ModPolicyOverrides,
		ModFolders:         req.ModFolders,
		ProfileNames:       req.ProfileNames,
		LockMod: func(name string) func() {
			return ts.s.lockMods(req.GameID, name)
		},
	}
	summary, ierr := transfer.Import(ctx, opts, emit)
	ts.s.invalidateInstalledArchiveCache(req.GameID)
	return summary, ierr
}

// refuseMountedOverwrites rejects OVERWRITE of the mounted profile or of any mod it has enabled.
func (ts *TransferService) refuseMountedOverwrites(req dto.ImportRequest, preview dto.ImportPreview) error {
	ts.s.mu.RLock()
	mm, hasMM := ts.s.mountMgrs[req.GameID]
	ms, hasMS := ts.s.mountStates[req.GameID]
	ts.s.mu.RUnlock()
	if !hasMM || !hasMS || !mm.IsMounted() {
		return nil
	}

	selectedMod := selectionSet(req.ModFolders)
	selectedProfile := selectionSet(req.ProfileNames)
	policyFor := func(folder string) dto.CollisionPolicy {
		if p, ok := req.ModPolicyOverrides[folder]; ok {
			return p
		}
		return req.Policy
	}

	if req.Policy == dto.PolicyOverwrite {
		for _, p := range preview.Profiles {
			if !p.Collision || (selectedProfile != nil && !selectedProfile[p.Name]) {
				continue
			}
			if p.Name == ms.profileName {
				return &TransferOverwriteMountedError{Name: p.Name}
			}
		}
	}

	_, entries, err := ts.s.profileMgr.Load(req.GameID, ms.profileName)
	if err != nil {
		return fmt.Errorf("loading mounted profile %q: %w", ms.profileName, err)
	}
	enabled := map[string]bool{}
	for _, e := range entries {
		if e.Enabled {
			enabled[e.Name] = true
		}
	}
	for _, m := range preview.Mods {
		if !m.Collision || (selectedMod != nil && !selectedMod[m.Folder]) {
			continue
		}
		if policyFor(m.Folder) == dto.PolicyOverwrite && enabled[m.Folder] {
			return &TransferOverwriteMountedError{Name: m.Folder}
		}
	}
	return nil
}

func (ts *TransferService) validGame(gameID string) error {
	ts.s.mu.RLock()
	_, ok := ts.s.config.Games[gameID]
	ts.s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	return nil
}

// selectionSet turns a request filter into a lookup set; nil means "all selected".
func selectionSet(names []string) map[string]bool {
	if len(names) == 0 {
		return nil
	}
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out
}
