package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

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
func (ts *TransferService) PreviewImport(ctx context.Context, gameID, archivePath string) (dto.ImportPreview, error) {
	if err := ts.validGame(gameID); err != nil {
		return dto.ImportPreview{}, err
	}
	return transfer.Preview(ctx, gameID, archivePath)
}

// ImportInstance waits for startup recovery within ctx, then applies an archive under the collision policy, refusing overwrites of mounted state.
func (ts *TransferService) ImportInstance(ctx context.Context, req dto.ImportRequest, emit func(dto.TransferProgress)) (dto.TransferSummary, error) {
	if err := ts.s.awaitRecoveryCtx(ctx); err != nil {
		return dto.TransferSummary{}, err
	}
	if err := ts.validGame(req.GameID); err != nil {
		return dto.TransferSummary{}, err
	}
	release, err := ts.s.acquireShared(req.GameID, dto.BusyOperationImport)
	if err != nil {
		return dto.TransferSummary{}, err
	}
	defer release()
	archive, err := os.Open(req.ArchivePath)
	if err != nil {
		return dto.TransferSummary{}, err
	}
	defer archive.Close()
	if req.ExpectedArchiveIdentity != "" {
		identity, err := transfer.ArchiveIdentity(archive)
		if err != nil {
			return dto.TransferSummary{}, err
		}
		if identity != req.ExpectedArchiveIdentity {
			return dto.TransferSummary{}, &transfer.BundleRejectedError{Reason: transfer.BundleRejectedChanged, Item: filepath.Base(req.ArchivePath)}
		}
	}
	preview, err := transfer.PreviewFromFile(ctx, req.GameID, archive)
	if err != nil {
		return dto.TransferSummary{}, err
	}
	if err := ts.refuseMountedOverwrites(req, preview); err != nil {
		return dto.TransferSummary{}, err
	}
	opts := transfer.ImportOptions{
		GameID:             req.GameID,
		ArchivePath:        req.ArchivePath,
		ArchiveFile:        archive,
		ArchiveIdentity:    req.ExpectedArchiveIdentity,
		Policy:             req.Policy,
		ModPolicyOverrides: req.ModPolicyOverrides,
		ModFolders:         req.ModFolders,
		ProfileNames:       req.ProfileNames,
		LockMod: func(name string) func() {
			return ts.s.lockMods(req.GameID, name)
		},
		LockProfiles: func() func() {
			return ts.s.lockProfiles(req.GameID)
		},
		LockState: func() func() {
			ts.s.mu.RLock()
			return ts.s.mu.RUnlock
		},
		CheckReplacement: func(root, name string) error {
			if root == config.ModsDir(req.GameID) {
				if err := checkModReplacement(root, name); err != nil {
					return err
				}
				policy := req.Policy
				if override, ok := req.ModPolicyOverrides[name]; ok {
					policy = override
				}
				if policy == dto.PolicyOverwrite {
					used, err := ts.s.svc.mods.mountedModUsedLocked(req.GameID, name)
					if err != nil {
						return err
					}
					if used {
						return &TransferOverwriteMountedError{Name: name}
					}
				}
			} else if root == config.ProfilesDir(req.GameID) && req.Policy == dto.PolicyOverwrite {
				if mm, state := ts.s.mountMgrs[req.GameID], ts.s.mountStates[req.GameID]; mm != nil && mm.IsMounted() && state.profileName == name {
					return &TransferOverwriteMountedError{Name: name}
				}
			}
			return nil
		},
	}
	summary, ierr := transfer.Import(ctx, opts, emit)
	ts.s.invalidateInstalledArchiveCache(req.GameID)
	return summary, ierr
}

// refuseMountedOverwrites rejects overwriting the mounted profile or a mod used by its farm or root deployment.
func (ts *TransferService) refuseMountedOverwrites(req dto.ImportRequest, preview dto.ImportPreview) error {
	defer ts.s.lockProfiles(req.GameID)()
	ts.s.mu.RLock()
	defer ts.s.mu.RUnlock()
	mm, hasMM := ts.s.mountMgrs[req.GameID]
	ms, hasMS := ts.s.mountStates[req.GameID]
	mountedProfile := hasMM && hasMS && mm.IsMounted()

	selectedMod := selectionSet(req.ModFolders)
	selectedProfile := selectionSet(req.ProfileNames)
	policyFor := func(folder string) dto.CollisionPolicy {
		if p, ok := req.ModPolicyOverrides[folder]; ok {
			return p
		}
		return req.Policy
	}

	if mountedProfile && req.Policy == dto.PolicyOverwrite {
		for _, p := range preview.Profiles {
			if !p.Collision || (selectedProfile != nil && !selectedProfile[p.Name]) {
				continue
			}
			if p.Name == ms.profileName {
				return &TransferOverwriteMountedError{Name: p.Name}
			}
		}
	}

	for _, m := range preview.Mods {
		if !m.Collision || (selectedMod != nil && !selectedMod[m.Folder]) || policyFor(m.Folder) != dto.PolicyOverwrite {
			continue
		}
		used, err := ts.s.svc.mods.mountedModUsedLocked(req.GameID, m.Folder)
		if err != nil {
			return err
		}
		if used {
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
