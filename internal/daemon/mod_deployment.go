package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/mod"
	"github.com/parka/gorganizer/internal/profile"
	"github.com/parka/gorganizer/internal/vfs"
)

type modListSnapshot struct {
	profile *profile.Profile
	entries []mod.ModListEntry
}

// snapshotModListsLocked loads every profile's modlist for a change that must be reversible; the caller holds the profile lock.
func (md *ModService) snapshotModListsLocked(gameID string) ([]modListSnapshot, error) {
	profiles, err := md.s.profileMgr.List(gameID)
	if err != nil {
		return nil, fmt.Errorf("listing profiles: %w", err)
	}
	var snapshots []modListSnapshot
	for _, p := range profiles {
		loaded, entries, err := md.s.profileMgr.Load(gameID, p.Name)
		if err != nil {
			return nil, fmt.Errorf("loading profile %q: %w", p.Name, err)
		}
		snapshots = append(snapshots, modListSnapshot{profile: loaded, entries: append([]mod.ModListEntry(nil), entries...)})
	}
	return snapshots, nil
}

// restoreModListsLocked restores each saved profile modlist; the caller holds the profile lock.
func (md *ModService) restoreModListsLocked(snapshots []modListSnapshot) error {
	var restoreErr error
	for _, snapshot := range snapshots {
		if err := md.s.profileMgr.Save(snapshot.profile, snapshot.entries); err != nil {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("restoring profile %q: %w", snapshot.profile.Name, err))
		}
	}
	return restoreErr
}

// checkMountedModChangeLocked refuses farm changes while a game, launch, tool, or recovery could still use the old mod; the caller holds s.mu.
func (md *ModService) checkMountedModChangeLocked(gameID, operation string) error {
	if md.s.recoveryPendingFor(gameID) != nil || md.s.refuseLoaderIntentLocked(gameID) != nil ||
		md.s.pendingAdmissionLocked(gameID, 0) != nil || md.s.applyBusyLocked(gameID) || md.s.unmountRunningLocked(gameID) {
		return &dto.GameRunningError{GameID: gameID, Operation: operation}
	}
	return nil
}

// rematerializeModChangeLocked applies a changed layer tree using the test fault hook when set; the caller holds s.mu.
func (md *ModService) rematerializeModChangeLocked(mm *vfs.MountManager) error {
	if md.s.modChangeRematerialize != nil {
		if err := md.s.modChangeRematerialize(mm); err != nil {
			return err
		}
	}
	return mm.ReMaterialize()
}

// restoreModFarmLocked restores the previously applied farm and root deployment, then its earlier desired tree; the caller holds s.mu.
func (md *ModService) restoreModFarmLocked(mm *vfs.MountManager, root *vfs.RootDeploymentManager, profileName string, applied, desired []vfs.Layer, wasDirty bool) error {
	var restoreErr error
	if err := mm.MarkDirty(applied); err != nil {
		restoreErr = errors.Join(restoreErr, fmt.Errorf("restoring applied layers: %w", err))
	} else if err := mm.ReMaterialize(); err != nil {
		restoreErr = errors.Join(restoreErr, fmt.Errorf("restoring Data farm: %w", err))
	}
	if _, err := root.Apply(applied, profileName); err != nil {
		restoreErr = errors.Join(restoreErr, fmt.Errorf("restoring game-root deployment: %w", err))
	}
	if wasDirty {
		if err := mm.MarkDirty(desired); err != nil {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("restoring pending mod changes: %w", err))
		}
	}
	return restoreErr
}

// uninstallModWithFarm removes every profile and farm reference to a mod and moves its folder to trash; the caller holds the install lock.
func (md *ModService) uninstallModWithFarm(gameID, modName string, force bool) (bool, string, error) {
	defer md.s.lockProfiles(gameID)()
	md.s.mu.Lock()
	defer md.s.mu.Unlock()
	if md.s.recoveryPendingFor(gameID) != nil || md.s.refuseLoaderIntentLocked(gameID) != nil {
		return false, "", &dto.GameRunningError{GameID: gameID, Operation: dto.GameRunningOperationUninstall}
	}
	used, err := md.mountedModUsedLocked(gameID, modName)
	if err != nil {
		return false, "", err
	}
	if !used {
		snapshots, err := md.snapshotModListsLocked(gameID)
		if err != nil {
			return false, "", err
		}
		if err := md.dropFromModListsLocked(gameID, modName, force); err != nil {
			return false, "", errors.Join(err, md.restoreModListsLocked(snapshots))
		}
		trash, err := md.moveModToTrashLocked(gameID, modName)
		return false, trash, err
	}
	if mm := md.s.mountMgrs[gameID]; mm == nil || !mm.IsMounted() {
		return false, "", &dto.GameRunningError{GameID: gameID, Operation: dto.GameRunningOperationUninstall}
	}
	if err := md.checkMountedModChangeLocked(gameID, dto.GameRunningOperationUninstall); err != nil {
		return false, "", err
	}
	release, err := md.s.reserveShared(gameID, dto.BusyOperationApply)
	if err != nil {
		return false, "", err
	}
	defer release()
	snapshots, err := md.snapshotModListsLocked(gameID)
	if err != nil {
		return false, "", err
	}
	if !force {
		var enabled []string
		for _, snapshot := range snapshots {
			for _, entry := range snapshot.entries {
				if entry.Name == modName && entry.Enabled {
					enabled = append(enabled, snapshot.profile.Name)
					break
				}
			}
		}
		if len(enabled) > 0 {
			return false, "", &ModInUseError{Name: modName, Profiles: enabled}
		}
	}
	mm := md.s.mountMgrs[gameID]
	ms := md.s.mountStates[gameID]
	gc, err := md.s.config.EffectiveGameConfig(gameID)
	if err != nil {
		return false, "", err
	}
	var current []mod.ModListEntry
	for _, snapshot := range snapshots {
		if snapshot.profile.Name == ms.profileName {
			current = snapshot.entries
			break
		}
	}
	previousDesired := md.s.svc.vfs.buildLayers(gameID, gc, current)
	previousApplied := mm.AppliedLayers()
	wasDirty := mm.IsDirty()
	without := make([]mod.ModListEntry, 0, len(current))
	for _, entry := range current {
		if entry.Name != modName {
			without = append(without, entry)
		}
	}
	layers := md.s.svc.vfs.buildLayers(gameID, gc, without)
	root, err := md.s.ensureRootDeploymentManager(gameID, gc)
	if err != nil {
		return false, "", fmt.Errorf("initializing game-root deployment: %w", err)
	}
	if err := mm.MarkDirty(layers); err != nil {
		return false, "", fmt.Errorf("preparing Data farm without mod: %w", err)
	}
	if err := md.rematerializeModChangeLocked(mm); err != nil {
		rollback := md.restoreModFarmLocked(mm, root, ms.profileName, previousApplied, previousDesired, wasDirty)
		return false, "", errors.Join(fmt.Errorf("rebuilding Data farm without mod: %w", err), rollback)
	}
	if _, err := root.Apply(layers, ms.profileName); err != nil {
		rollback := md.restoreModFarmLocked(mm, root, ms.profileName, previousApplied, previousDesired, wasDirty)
		return false, "", errors.Join(fmt.Errorf("applying game-root deployment without mod: %w", err), rollback)
	}
	if err := md.dropFromModListsLocked(gameID, modName, true); err != nil {
		rollback := md.restoreModListsLocked(snapshots)
		rollback = errors.Join(rollback, md.restoreModFarmLocked(mm, root, ms.profileName, previousApplied, previousDesired, wasDirty))
		return false, "", errors.Join(fmt.Errorf("updating modlists: %w", err), rollback)
	}
	md.s.publishGuarded(dto.StatusEventResult{VFSStatus: md.s.svc.vfs.vfsStatus(gameID, gc, ms.profileName, mm, without)})
	trash, err := md.moveModToTrashLocked(gameID, modName)
	return true, trash, err
}

// moveModToTrashLocked moves an uninstalled mod into a hidden sibling and syncs the mods directory; the caller holds s.mu and the profile lock.
func (md *ModService) moveModToTrashLocked(gameID, modName string) (string, error) {
	modsDir := config.ModsDir(gameID)
	trash := filepath.Join(modsDir, ".gorganizer-trash-"+uuid.NewString())
	modDir := filepath.Join(modsDir, modName)
	rename := os.Rename
	if md.s.uninstallRename != nil {
		rename = md.s.uninstallRename
	}
	if err := rename(modDir, trash); err != nil {
		return "", fmt.Errorf("could not finish removing the mod: %w", err)
	}
	if err := atomicfile.SyncDir(modsDir); err != nil {
		return trash, fmt.Errorf("could not save the mod removal: %w", err)
	}
	return trash, nil
}

// removeModFolder deletes a mod directory, retrying a failed removal once.
func removeModFolder(dir string) error {
	var removeErr error
	for attempt := 0; attempt < 2; attempt++ {
		if err := os.RemoveAll(dir); err == nil {
			return nil
		} else {
			removeErr = err
			time.Sleep(100 * time.Millisecond)
		}
	}
	return fmt.Errorf("removing mod folder: %w", removeErr)
}

// renameModWithFarm renames a mod and rebuilds any mounted farm that references its old name; the caller holds both install locks.
func (md *ModService) renameModWithFarm(gameID, oldName, newName, src, dst string) error {
	defer md.s.lockProfiles(gameID)()
	md.s.mu.Lock()
	defer md.s.mu.Unlock()
	if md.s.recoveryPendingFor(gameID) != nil || md.s.refuseLoaderIntentLocked(gameID) != nil {
		return &dto.GameRunningError{GameID: gameID, Operation: dto.GameRunningOperationRename}
	}
	used, err := md.mountedModUsedLocked(gameID, oldName)
	if err != nil {
		return err
	}
	if used {
		if mm := md.s.mountMgrs[gameID]; mm == nil || !mm.IsMounted() {
			return &dto.GameRunningError{GameID: gameID, Operation: dto.GameRunningOperationRename}
		}
		if err := md.checkMountedModChangeLocked(gameID, dto.GameRunningOperationRename); err != nil {
			return err
		}
	}
	var release func()
	if used {
		release, err = md.s.reserveShared(gameID, dto.BusyOperationApply)
		if err != nil {
			return err
		}
		defer release()
	}
	snapshots, err := md.snapshotModListsLocked(gameID)
	if err != nil {
		return err
	}
	renamed, err := md.renameModFolder(gameID, oldName, newName, src, dst)
	if err != nil {
		if renamed {
			return errors.Join(err, md.restoreRenamedModLocked(src, dst, snapshots))
		}
		return err
	}
	if used {
		mm := md.s.mountMgrs[gameID]
		ms := md.s.mountStates[gameID]
		gc, configErr := md.s.config.EffectiveGameConfig(gameID)
		if configErr != nil {
			return errors.Join(configErr, md.restoreRenamedModLocked(src, dst, snapshots))
		}
		previousApplied := mm.AppliedLayers()
		wasDirty := mm.IsDirty()
		var previousEntries []mod.ModListEntry
		for _, snapshot := range snapshots {
			if snapshot.profile.Name == ms.profileName {
				previousEntries = snapshot.entries
				break
			}
		}
		previousDesired := md.s.svc.vfs.buildLayers(gameID, gc, previousEntries)
		_, entries, loadErr := md.s.profileMgr.Load(gameID, ms.profileName)
		if loadErr != nil {
			return errors.Join(loadErr, md.restoreRenamedModLocked(src, dst, snapshots))
		}
		layers := md.s.svc.vfs.buildLayers(gameID, gc, entries)
		root, rootErr := md.s.ensureRootDeploymentManager(gameID, gc)
		if rootErr != nil {
			return errors.Join(rootErr, md.restoreRenamedModLocked(src, dst, snapshots))
		}
		err = mm.MarkDirty(layers)
		if err == nil {
			err = md.rematerializeModChangeLocked(mm)
		}
		if err == nil {
			_, err = root.Apply(layers, ms.profileName)
		}
		if err != nil {
			rollback := md.restoreRenamedModLocked(src, dst, snapshots)
			rollback = errors.Join(rollback, md.restoreModFarmLocked(mm, root, ms.profileName, previousApplied, previousDesired, wasDirty))
			return errors.Join(fmt.Errorf("rebuilding deployed mod after rename: %w", err), rollback)
		}
		md.s.publishGuarded(dto.StatusEventResult{VFSStatus: md.s.svc.vfs.vfsStatus(gameID, gc, ms.profileName, mm, entries)})
	}
	meta, _ := download.LoadModMetadata(dst)
	if meta != nil {
		meta.Folder = newName
		if meta.Name == oldName {
			meta.Name = newName
		}
		_ = download.SaveModMetadata(dst, meta)
	}
	md.s.invalidateInstalledArchiveCache(gameID)
	if !used {
		md.markMountedProfileDirtyLocked(gameID)
	}
	return nil
}

// restoreRenamedModLocked restores the old folder name and every modlist; the caller holds s.mu and the profile lock.
func (md *ModService) restoreRenamedModLocked(src, dst string, snapshots []modListSnapshot) error {
	var rollback error
	if err := os.Rename(dst, src); err != nil {
		rollback = fmt.Errorf("restoring mod folder: %w", err)
	}
	return errors.Join(rollback, md.restoreModListsLocked(snapshots))
}

// markMountedProfileDirtyLocked rebuilds the desired farm tree for a caller that holds s.mu and the profile lock.
func (md *ModService) markMountedProfileDirtyLocked(gameID string) {
	mm := md.s.mountMgrs[gameID]
	ms, ok := md.s.mountStates[gameID]
	if !ok || mm == nil || !mm.IsMounted() {
		return
	}
	gc, err := md.s.config.EffectiveGameConfig(gameID)
	if err != nil {
		return
	}
	_, entries, err := md.s.profileMgr.Load(gameID, ms.profileName)
	if err != nil {
		return
	}
	if err := mm.MarkDirty(md.s.svc.vfs.buildLayers(gameID, gc, entries)); err == nil {
		md.s.publishGuarded(dto.StatusEventResult{VFSStatus: md.s.svc.vfs.vfsStatus(gameID, gc, ms.profileName, mm, entries)})
	}
}
