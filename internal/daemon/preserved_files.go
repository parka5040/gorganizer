package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/vfs"
	"golang.org/x/sys/unix"
)

// maintenanceDataPath returns the configured game's physical deploy folder.
func (s *session) maintenanceDataPath(gameID string) (string, error) {
	if _, ok := s.gameConfigSnapshot(gameID); !ok {
		return "", fmt.Errorf("game %q: %w", gameID, os.ErrNotExist)
	}
	effective, err := s.effectiveGameConfigSnapshot(gameID)
	if err != nil {
		return "", err
	}
	subpath := effective.DataSubpath
	if subpath == "" {
		subpath = "Data"
	}
	return filepath.Join(effective.InstallPath, subpath), nil
}

// SetSteamMaintenance pauses mods for a Steam update or resumes them after Steam becomes idle.
func (vs *VFSService) SetSteamMaintenance(gameID string, enabled, verificationConfirmed bool) (*dto.VFSStatusResult, error) {
	if err := vs.s.awaitRecovery(); err != nil {
		return nil, err
	}
	if err := vs.s.refuseWhenShuttingDown("steam_maintenance"); err != nil {
		return nil, err
	}
	if _, ok := vs.s.gameConfigSnapshot(gameID); !ok {
		return nil, fmt.Errorf("game %q: %w", gameID, os.ErrNotExist)
	}
	vs.s.mu.Lock()
	defer vs.s.mu.Unlock()
	gc, err := vs.s.config.EffectiveGameConfig(gameID)
	if err != nil {
		return nil, err
	}
	subpath := gc.DataSubpath
	if subpath == "" {
		subpath = "Data"
	}
	dataPath := filepath.Join(vs.s.mountInstallPath(gc), subpath)
	{
		checkedRoots := make(map[*vfs.RootDeploymentManager]bool)
		for otherID := range vs.s.config.Games {
			if otherID == gameID {
				continue
			}
			other, err := vs.s.config.EffectiveGameConfig(otherID)
			if err != nil {
				return nil, err
			}
			otherSubpath := other.DataSubpath
			if otherSubpath == "" {
				otherSubpath = "Data"
			}
			if filepath.Clean(filepath.Join(vs.s.mountInstallPath(other), otherSubpath)) != filepath.Clean(dataPath) {
				continue
			}
			if mm := vs.s.mountMgrs[otherID]; mm != nil && mm.IsMounted() {
				return nil, &dto.OperationBusyError{GameID: gameID, Operation: dto.BusyOperationMounted, Holder: otherID}
			}
			root := vs.s.rootDeployMgrs[otherID]
			if root == nil {
				root, err = vs.s.ensureRootDeploymentManager(otherID, other)
				if err != nil {
					return nil, err
				}
			}
			if root != nil && !checkedRoots[root] {
				checkedRoots[root] = true
				manifest, err := root.ActiveManifest()
				if err != nil {
					return nil, err
				}
				if manifest != nil && manifest.GameID != gameID {
					return nil, &dto.OperationBusyError{GameID: gameID, Operation: dto.BusyOperationMounted, Holder: otherID}
				}
			}
		}
	}
	if !enabled {
		if state, _ := vs.s.steamStatusLocked(gameID, dataPath); state == dto.SteamMaintenanceVerify {
			if mm := vs.s.mountMgrs[gameID]; mm != nil && mm.IsMounted() {
				return nil, &dto.SteamMaintenanceError{GameID: gameID, Reason: "verify"}
			}
			if root := vs.s.rootDeployMgrs[gameID]; root != nil {
				manifest, err := root.ActiveManifest()
				if err != nil {
					return nil, err
				}
				if manifest != nil {
					return nil, &dto.SteamMaintenanceError{GameID: gameID, Reason: "verify"}
				}
			}
		}
	}
	marker, err := vfs.ReadMaintenance(dataPath)
	if err != nil {
		return nil, fmt.Errorf("reading Steam maintenance: %w", err)
	}
	if marker != nil && marker.GameID != gameID {
		return nil, fmt.Errorf("Steam maintenance belongs to a different game")
	}
	if enabled {
		if marker == nil || vs.s.mountMgrs[gameID] != nil && vs.s.mountMgrs[gameID].IsMounted() {
			if err := vs.unmountVFSLocked(gameID, true); err != nil {
				return nil, err
			}
		}
		marker, err = vfs.ReadMaintenance(dataPath)
		if err != nil {
			return nil, fmt.Errorf("reading Steam maintenance after unmount: %w", err)
		}
		if marker != nil && marker.GameID != gameID {
			return nil, fmt.Errorf("Steam maintenance belongs to a different game")
		}
		if marker == nil {
			marker = &vfs.MaintenanceMarker{SchemaVersion: 1, GameID: gameID, Reason: "user", CreatedAt: vs.s.clock().UTC()}
			if err := writeMaintenanceMarker(dataPath, marker); err != nil {
				return nil, err
			}
		}
	} else {
		if marker != nil && marker.Reason == "verify" && !verificationConfirmed {
			return nil, ErrVerificationConfirmationRequired
		}
		state, readErr := vs.s.readSteamAppState(vs.s.mountInstallPath(gc), gc.SteamAppID)
		if readErr == nil && !state.Idle() {
			return nil, &dto.SteamMaintenanceError{GameID: gameID, Reason: "busy"}
		}
		if readErr != nil && !verificationConfirmed {
			return nil, ErrVerificationConfirmationRequired
		}
		if marker != nil {
			if err := atomicfile.RemoveDurable(vfs.MaintenancePath(dataPath)); err != nil {
				return nil, fmt.Errorf("finishing Steam maintenance: %w", err)
			}
		}
	}
	status := vs.statusLocked(gameID)
	vs.s.publishGuarded(dto.StatusEventResult{VFSStatus: status})
	return status, nil
}

// writeMaintenanceMarker durably replaces a maintenance record.
func writeMaintenanceMarker(dataPath string, marker *vfs.MaintenanceMarker) error {
	body, err := json.Marshal(marker)
	if err != nil {
		return fmt.Errorf("encoding Steam maintenance: %w", err)
	}
	if _, err := atomicfile.WriteFileDurable(vfs.MaintenancePath(dataPath), body, 0644); err != nil {
		return fmt.Errorf("writing Steam maintenance: %w", err)
	}
	return nil
}

// preservedBatch locates a real preserved batch belonging to the selected game.
func preservedBatch(dataPath, gameID, batchID string) (vfs.PreservedBatch, error) {
	if uuid.Validate(batchID) != nil {
		return vfs.PreservedBatch{}, &UnsafePathError{Field: "batch_id"}
	}
	batches, err := vfs.ListPreservedBatches(dataPath)
	if err != nil {
		return vfs.PreservedBatch{}, fmt.Errorf("listing preserved files: %w", err)
	}
	for _, batch := range batches {
		if batch.BatchID == batchID && batch.GameID == gameID {
			info, err := os.Lstat(batch.Path)
			if err != nil || !info.IsDir() {
				return vfs.PreservedBatch{}, &UnsafePathError{Field: "batch_id"}
			}
			return batch, nil
		}
	}
	return vfs.PreservedBatch{}, fmt.Errorf("preserved batch %q: %w", batchID, os.ErrNotExist)
}

// checkedPreservedFile validates every source component and returns the selected regular file.
func checkedPreservedFile(batch vfs.PreservedBatch, rel string) (string, os.FileInfo, error) {
	if rel == "" || !filepath.IsLocal(rel) || filepath.Clean(rel) != rel || filepath.ToSlash(rel) != rel || strings.Contains(rel, "\\") ||
		!slices.Contains(batch.Files, rel) || strings.EqualFold(rel, "metadata.yaml") || strings.HasPrefix(strings.ToLower(rel), "metadata.yaml/") {
		return "", nil, &UnsafePathError{Field: "relative_paths"}
	}
	root := filepath.Join(batch.Path, "files")
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() {
		return "", nil, &UnsafePathError{Field: "relative_paths"}
	}
	current := root
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err = os.Lstat(current)
		if err != nil || i < len(parts)-1 && !info.IsDir() || i == len(parts)-1 && !info.Mode().IsRegular() {
			return "", nil, &UnsafePathError{Field: "relative_paths"}
		}
	}
	return current, info, nil
}

// ImportPreservedFiles copies selected retained files into a new disabled mod without consuming the batch.
func (vs *VFSService) ImportPreservedFiles(gameID, batchID, modName string, relativePaths []string) (string, int, error) {
	if err := vs.s.awaitRecovery(); err != nil {
		return "", 0, err
	}
	if err := vs.s.refuseWhenShuttingDown("import_preserved_files"); err != nil {
		return "", 0, err
	}
	dataPath, err := vs.s.maintenanceDataPath(gameID)
	if err != nil {
		return "", 0, err
	}
	if err := download.ValidateTargetModName(modName); err != nil {
		return "", 0, err
	}
	if uuid.Validate(batchID) != nil {
		return "", 0, &UnsafePathError{Field: "batch_id"}
	}
	if len(relativePaths) == 0 {
		return "", 0, &UnsafePathError{Field: "relative_paths"}
	}
	defer vs.s.lockMods(gameID, modName, ".preserved-"+batchID)()
	batch, err := preservedBatch(dataPath, gameID, batchID)
	if err != nil {
		return "", 0, err
	}
	modsDir := config.ModsDir(gameID)
	final := filepath.Join(modsDir, modName)
	if _, err := os.Lstat(final); err == nil {
		return "", 0, &ModCollisionError{Name: modName}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", 0, fmt.Errorf("checking mod folder: %w", err)
	}
	type selectedFile struct {
		path string
		rel  string
		perm os.FileMode
	}
	selected := make([]selectedFile, 0, len(relativePaths))
	seen := make(map[string]bool, len(relativePaths))
	for _, rel := range relativePaths {
		if seen[rel] {
			return "", 0, &UnsafePathError{Field: "relative_paths"}
		}
		seen[rel] = true
		path, info, err := checkedPreservedFile(batch, rel)
		if err != nil {
			return "", 0, err
		}
		selected = append(selected, selectedFile{path: path, rel: rel, perm: info.Mode().Perm()})
	}
	if err := ensureModsDir(gameID); err != nil {
		return "", 0, err
	}
	stage, err := os.MkdirTemp(modsDir, ".stage-preserved-")
	if err != nil {
		return "", 0, fmt.Errorf("staging preserved files: %w", err)
	}
	defer os.RemoveAll(stage)
	for _, file := range selected {
		dir := filepath.Dir(filepath.Join(stage, filepath.FromSlash(file.rel)))
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", 0, fmt.Errorf("creating mod folders: %w", err)
		}
		if _, err := atomicfile.CopyFileDurable(file.path, filepath.Join(stage, filepath.FromSlash(file.rel)), file.perm, false); err != nil {
			if errors.Is(err, unix.ELOOP) || errors.Is(err, os.ErrInvalid) {
				return "", 0, &UnsafePathError{Field: "relative_paths"}
			}
			return "", 0, fmt.Errorf("copying preserved file: %w", err)
		}
	}
	for _, file := range selected {
		dir := filepath.Dir(filepath.Join(stage, filepath.FromSlash(file.rel)))
		for dir != stage {
			if err := atomicfile.SyncDir(dir); err != nil {
				return "", 0, fmt.Errorf("syncing mod folders: %w", err)
			}
			dir = filepath.Dir(dir)
		}
	}
	if err := download.SaveModMetadata(stage, &download.ModMetadata{Name: modName, Folder: modName, FileCount: len(selected), Files: relativePaths}); err != nil {
		return "", 0, fmt.Errorf("recording imported mod: %w", err)
	}
	if err := atomicfile.SyncDir(stage); err != nil {
		return "", 0, fmt.Errorf("syncing imported mod: %w", err)
	}
	if err := unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, final, unix.RENAME_NOREPLACE); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return "", 0, &ModCollisionError{Name: modName}
		}
		return "", 0, fmt.Errorf("publishing imported mod: %w", err)
	}
	if err := atomicfile.SyncDir(modsDir); err != nil {
		return modName, len(selected), fmt.Errorf("syncing published mod: %w", err)
	}
	if err := vs.s.svc.mods.ensureInModList(gameID, modName); err != nil {
		return modName, len(selected), &download.ModRegistrationError{Mod: modName, Err: err}
	}
	return modName, len(selected), nil
}

// DeletePreservedBatch removes a retained batch and drops its ID from the maintenance marker.
func (vs *VFSService) DeletePreservedBatch(gameID, batchID string) (*dto.VFSStatusResult, error) {
	if err := vs.s.awaitRecovery(); err != nil {
		return nil, err
	}
	if err := vs.s.refuseWhenShuttingDown("delete_preserved_batch"); err != nil {
		return nil, err
	}
	dataPath, err := vs.s.maintenanceDataPath(gameID)
	if err != nil {
		return nil, err
	}
	if uuid.Validate(batchID) != nil {
		return nil, &UnsafePathError{Field: "batch_id"}
	}
	defer vs.s.lockMods(gameID, ".preserved-"+batchID)()
	batch, err := preservedBatch(dataPath, gameID, batchID)
	if err != nil {
		return nil, err
	}
	vs.s.mu.Lock()
	marker, err := vfs.ReadMaintenance(dataPath)
	if err == nil && marker != nil {
		if marker.GameID != gameID {
			err = fmt.Errorf("Steam maintenance belongs to a different game")
		} else if slices.Contains(marker.BatchIDs, batchID) {
			marker.BatchIDs = slices.DeleteFunc(marker.BatchIDs, func(id string) bool { return id == batchID })
			err = writeMaintenanceMarker(dataPath, marker)
		}
	}
	vs.s.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("updating Steam maintenance: %w", err)
	}
	rootInfo, rootErr := os.Lstat(vfs.PreservedDir(dataPath))
	batchInfo, batchErr := os.Lstat(batch.Path)
	if rootErr != nil || batchErr != nil || !rootInfo.IsDir() || !batchInfo.IsDir() {
		return nil, &UnsafePathError{Field: "batch_id"}
	}
	if err := os.RemoveAll(batch.Path); err != nil {
		return nil, fmt.Errorf("deleting preserved files: %w", err)
	}
	if err := atomicfile.SyncDir(vfs.PreservedDir(dataPath)); err != nil {
		return nil, fmt.Errorf("syncing preserved files: %w", err)
	}
	return vs.GetVFSStatus(gameID)
}
