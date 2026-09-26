package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/gamedef"
	"github.com/parka/gorganizer/internal/mod"
	"github.com/parka/gorganizer/internal/profile"
	"github.com/parka/gorganizer/internal/vfs"
)

const (
	stageSweepMargin  = 2 * time.Second
	guiStagePrefix    = ".stage-ui-"
	guiStageRetention = 24 * time.Hour
)

// vfsStatus builds a VFSStatusResult from the mount's live generation counters.
func (vs *VFSService) vfsStatus(gameID string, gc config.GameConfig, profileName string, mm *vfs.MountManager, entries []mod.ModListEntry) *dto.VFSStatusResult {
	enabled := 0
	for _, e := range entries {
		if e.Enabled {
			enabled++
		}
	}
	subpath := gc.DataSubpath
	if subpath == "" {
		subpath = "Data"
	}
	fileCount := 0
	if t := mm.Tree(); t != nil {
		fileCount, _ = t.Stats()
	}
	applied, desired := mm.Generations()
	return &dto.VFSStatusResult{
		Mounted:         mm.IsMounted(),
		GameID:          gameID,
		ProfileName:     profileName,
		MountPoint:      filepath.Join(gc.InstallPath, subpath),
		EnabledModCount: enabled,
		TotalFileCount:  fileCount,
		Dirty:           mm.IsDirty(),
		AppliedGen:      applied,
		DesiredGen:      desired,
	}
}

func (vs *VFSService) MountVFS(gameID, profileName string) (*dto.VFSStatusResult, error) {
	return vs.mountVFSWithSwap(gameID, profileName, false)
}

// MountVFSWithSwap is the auto-swap variant for gameID's mutex group.
func (vs *VFSService) MountVFSWithSwap(gameID, profileName string) (*dto.VFSStatusResult, error) {
	return vs.mountVFSWithSwap(gameID, profileName, true)
}

func (vs *VFSService) mountVFSWithSwap(gameID, profileName string, autoSwap bool) (*dto.VFSStatusResult, error) {
	if err := vs.s.awaitRecovery(); err != nil {
		return nil, err
	}
	vs.s.mu.Lock()
	defer vs.s.mu.Unlock()

	if pending := vs.s.recoveryPendingFor(gameID); pending != nil {
		return nil, fmt.Errorf("recovery pending for %s: %s — confirm via the GUI prompt or `gorganizerctl recover-confirm` first",
			gameID, pending.Reason)
	}
	if err := vs.s.refuseLoaderIntentLocked(gameID); err != nil {
		return nil, err
	}
	release, err := vs.s.reserveShared(gameID, dto.BusyOperationMount)
	if err != nil {
		return nil, err
	}
	defer release()

	if conflict := vs.s.findMutexConflict(gameID); conflict != "" {
		if !autoSwap {
			return nil, &VFSMutexError{
				GameID:      gameID,
				Conflicting: conflict,
				Group:       mutexGroupOf(gameID),
			}
		}
		if conflictMM, ok := vs.s.mountMgrs[conflict]; ok && conflictMM.IsMounted() {
			if vs.s.teardownBusyLocked(conflict) {
				return nil, fmt.Errorf("cannot auto-swap while %s is running", conflict)
			}
			conflictGC, err := vs.s.config.EffectiveGameConfig(conflict)
			if err != nil {
				return nil, err
			}
			conflictState := vs.s.mountStates[conflict]
			if rootManager, rootOK := vs.s.rootDeployMgrs[conflict]; rootOK {
				if _, err := rootManager.Deactivate(); err != nil {
					return nil, fmt.Errorf("auto-swap root deactivate of %s failed: %w", conflict, err)
				}
			}
			if err := conflictMM.Deactivate(); err != nil {
				if restoreErr := vs.s.applyRootDeployment(conflict, conflictGC, conflictState.profileName); restoreErr != nil {
					return nil, fmt.Errorf("auto-swap deactivate of %s failed: %v; restoring root deployment also failed: %w", conflict, err, restoreErr)
				}
				return nil, fmt.Errorf("auto-swap deactivate of %s failed: %w", conflict, err)
			}
			delete(vs.s.mountStates, conflict)
			vs.s.setSteamLaunched(conflict, false)
			vs.s.publishGuarded(dto.StatusEventResult{VFSStatus: &dto.VFSStatusResult{GameID: conflict}})
			slog.Info("auto-swap: deactivated conflicting VFS", "deactivated", conflict, "now_activating", gameID)
		}
	}

	gc, ok := vs.s.config.Games[gameID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	if gc.LinkedFromGameID != "" {
		if _, parentOk := vs.s.config.Games[gc.LinkedFromGameID]; !parentOk {
			return nil, &ErrLinkedParentMissing{
				GameID:       gameID,
				ParentGameID: gc.LinkedFromGameID,
			}
		}
	}
	effectiveGC, err := vs.s.config.EffectiveGameConfig(gameID)
	if err != nil {
		return nil, err
	}

	mm := vs.s.ensureMountManager(gameID, effectiveGC)
	if mm.IsMounted() {
		return vs.alreadyMountedStatus(gameID, profileName, effectiveGC, mm)
	}
	if err := vs.ensureOptionalDataDir(gameID, mm); err != nil {
		return nil, err
	}

	_, entries, err := vs.s.profileMgr.Load(gameID, profileName)
	if err != nil {
		return nil, fmt.Errorf("loading profile %q: %w", profileName, err)
	}

	layers := vs.buildLayers(gameID, effectiveGC, entries)

	rootManager, err := vs.s.ensureRootDeploymentManager(gameID, effectiveGC)
	if err != nil {
		return nil, fmt.Errorf("initializing game-root deployment: %w", err)
	}
	if _, err := rootManager.Apply(layers, profileName); err != nil {
		return nil, fmt.Errorf("applying game-root deployment: %w", err)
	}
	if err := mm.Activate(layers, profileName); err != nil {
		if _, rootErr := rootManager.Deactivate(); rootErr != nil {
			return nil, fmt.Errorf("activating Data VFS failed: %v; rolling back game-root deployment also failed: %w", err, rootErr)
		}
		return nil, err
	}

	vs.s.mountStates[gameID] = mountState{profileName: profileName}

	st := vs.vfsStatus(gameID, effectiveGC, profileName, mm, entries)
	vs.s.publishGuarded(dto.StatusEventResult{VFSStatus: st})
	return st, nil
}

// ensureOptionalDataDir creates a missing registry deploy folder before mounting a game whose deploy folder is optional.
func (vs *VFSService) ensureOptionalDataDir(gameID string, mm *vfs.MountManager) error {
	def, ok := gamedef.ByID(gameID)
	if !ok || !def.DataDirOptional {
		return nil
	}

	gc, ok := vs.s.config.Games[gameID]
	if !ok {
		return fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	installPath := vs.s.mountInstallPath(gc)
	dataPath := mm.DataPath()
	if !filepath.IsAbs(installPath) {
		return fmt.Errorf("refusing to create deploy folder for %s: install path %q is not absolute", gameID, installPath)
	}
	wantPath := filepath.Join(installPath, filepath.FromSlash(def.DataSubpath))
	if filepath.Clean(dataPath) != wantPath {
		return fmt.Errorf("refusing to create deploy folder %s for %s: the registry deploy folder is %s", dataPath, gameID, wantPath)
	}

	info, err := os.Lstat(dataPath)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("deploy folder %s for %s exists but is not a real directory; move it aside before mounting", dataPath, gameID)
		}
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("checking optional deploy folder %s: %w", dataPath, err)
	}

	if _, err := os.Lstat(mm.BackupPath()); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("checking optional deploy backup %s: %w", mm.BackupPath(), err)
	}
	intentPath := vfs.ActivationIntentPath(dataPath)
	if _, err := vfs.ReadIntent(intentPath); err == nil {
		return nil
	} else if !errors.Is(err, vfs.ErrIntentMissing) {
		return fmt.Errorf("activation intent %s for %s is unreadable or corrupt; run `gorganizerctl recover --game %s` with the daemon stopped before mounting: %w",
			intentPath, gameID, gameID, err)
	}

	if err := os.Mkdir(dataPath, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil
		}
		return fmt.Errorf("creating optional deploy folder %s: %w", dataPath, err)
	}
	slog.Info("created optional deploy folder", "game", gameID, "path", dataPath)
	return nil
}

// alreadyMountedStatus answers a mount request for a game whose farm is already active without touching its root deployment.
func (vs *VFSService) alreadyMountedStatus(gameID, profileName string, gc config.GameConfig, mm *vfs.MountManager) (*dto.VFSStatusResult, error) {
	ms, ok := vs.s.mountStates[gameID]
	if !ok || ms.profileName != profileName {
		return nil, fmt.Errorf("%w: %s is mounted with another profile; unmount it first", vfs.ErrAlreadyMounted, gameID)
	}
	_, entries, err := vs.s.profileMgr.Load(gameID, profileName)
	if err != nil {
		return nil, fmt.Errorf("loading profile %q: %w", profileName, err)
	}
	return vs.vfsStatus(gameID, gc, profileName, mm, entries), nil
}

func (vs *VFSService) UnmountVFS(gameID string) error {
	vs.s.mu.Lock()
	defer vs.s.mu.Unlock()

	mm, ok := vs.s.mountMgrs[gameID]
	if !ok {
		return fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	release, err := vs.s.reserveShared(gameID, dto.BusyOperationUnmount)
	if err != nil {
		return err
	}
	defer release()
	if vs.s.trackedMountBusy(gameID) {
		return fmt.Errorf("cannot unmount while %s has a tracked game or tool process", gameID)
	}
	gc, err := vs.s.config.EffectiveGameConfig(gameID)
	if err != nil {
		return err
	}
	state := vs.s.mountStates[gameID]
	if rootManager, rootOK := vs.s.rootDeployMgrs[gameID]; rootOK {
		if _, err := rootManager.Deactivate(); err != nil {
			return fmt.Errorf("deactivating game-root deployment: %w", err)
		}
	}
	if err := mm.Deactivate(); err != nil {
		if restoreErr := vs.s.applyRootDeployment(gameID, gc, state.profileName); restoreErr != nil {
			return fmt.Errorf("deactivating Data VFS failed: %v; restoring game-root deployment also failed: %w", err, restoreErr)
		}
		return err
	}
	delete(vs.s.mountStates, gameID)
	vs.s.setSteamLaunched(gameID, false)
	vs.s.publishGuarded(dto.StatusEventResult{VFSStatus: &dto.VFSStatusResult{GameID: gameID}})
	return nil
}

// GetVFSStatus reports gameID's mount with the same profile, mount point, mod and file counts, dirty flag, and generations the status stream carries.
func (vs *VFSService) GetVFSStatus(gameID string) (*dto.VFSStatusResult, error) {
	vs.s.mu.RLock()
	defer vs.s.mu.RUnlock()

	mm, ok := vs.s.mountMgrs[gameID]
	if !ok {
		return &dto.VFSStatusResult{GameID: gameID}, nil
	}
	gc, err := vs.s.config.EffectiveGameConfig(gameID)
	if err != nil {
		gc = vs.s.config.Games[gameID]
	}
	profileName := ""
	var entries []mod.ModListEntry
	if ms, ok := vs.s.mountStates[gameID]; ok {
		profileName = ms.profileName
		if mm.IsMounted() {
			_, loaded, loadErr := vs.s.profileMgr.Load(gameID, profileName)
			if loadErr != nil {
				slog.Warn("could not read the mounted profile for a VFS status", "game", gameID, "profile", profileName, "err", loadErr)
			}
			entries = loaded
		}
	}
	return vs.vfsStatus(gameID, gc, profileName, mm, entries), nil
}

// RestoreFromBackup resolves one pending recovery of gameID per confirmation, the mod-loader entry first, re-announcing any entry that remains, and refuses once shutdown began.
func (vs *VFSService) RestoreFromBackup(gameID string) error {
	if err := vs.s.refuseWhenShuttingDown("restore_from_backup"); err != nil {
		return err
	}
	loaderHandled, err := vs.s.retryLoaderRecovery(gameID)
	if err != nil {
		return err
	}
	if loaderHandled {
		vs.s.mu.RLock()
		remaining := vs.s.recoveryPendingFor(gameID)
		vs.s.mu.RUnlock()
		if remaining != nil {
			vs.s.publishGuarded(dto.StatusEventResult{RecoveryPending: remaining})
		}
		return nil
	}
	vs.s.pendingRecoveriesMu.Lock()
	rootPending := vs.s.rootPendingRecoveries[gameID]
	vs.s.pendingRecoveriesMu.Unlock()
	if rootPending != nil {
		vs.s.mu.RLock()
		manager, ok := vs.s.rootDeployMgrs[gameID]
		vs.s.mu.RUnlock()
		if !ok {
			return fmt.Errorf("no game-root deployment manager for %s", gameID)
		}
		outcome, err := manager.Recover()
		if err != nil {
			return fmt.Errorf("recovering game-root deployment: %w", err)
		}
		if outcome.Pending != nil {
			return fmt.Errorf("game-root drift remains at %s: %s", outcome.Pending.Path, outcome.Pending.Reason)
		}
		if _, err := manager.Deactivate(); err != nil {
			return fmt.Errorf("restoring game-root deployment: %w", err)
		}
		vs.s.mu.RLock()
		vs.s.pendingRecoveriesMu.Lock()
		for affectedGameID, affectedManager := range vs.s.rootDeployMgrs {
			if affectedManager == manager {
				delete(vs.s.rootPendingRecoveries, affectedGameID)
			}
		}
		vs.s.pendingRecoveriesMu.Unlock()
		vs.s.mu.RUnlock()
		return nil
	}
	vs.s.mu.RLock()
	mm, ok := vs.s.mountMgrs[gameID]
	vs.s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("no mount manager for %s", gameID)
	}
	resolved, err := filepath.Abs(mm.DataPath())
	if err != nil {
		resolved = mm.DataPath()
	}

	vs.s.pendingRecoveriesMu.Lock()
	pending, exists := vs.s.pendingRecoveries[resolved]
	siblings := append([]string{}, vs.s.gamesAtPath[resolved]...)
	vs.s.pendingRecoveriesMu.Unlock()
	if !exists {
		return fmt.Errorf("no recovery pending for %s (path %s)", gameID, resolved)
	}

	if err := vfs.RestoreFromBackup(pending.DataPath); err != nil {
		return fmt.Errorf("restoring %s: %w", pending.DataPath, err)
	}

	vs.s.pendingRecoveriesMu.Lock()
	delete(vs.s.pendingRecoveries, resolved)
	delete(vs.s.gamesAtPath, resolved)
	vs.s.pendingRecoveriesMu.Unlock()

	slog.Info("restore from backup completed via user consent",
		"game", gameID, "path", pending.DataPath, "siblings", siblings)
	for _, sibling := range siblings {
		vs.s.publishGuarded(dto.StatusEventResult{Info: fmt.Sprintf("recovery resolved for %s", sibling)})
	}
	return nil
}

func (vs *VFSService) RebuildVFS(gameID string) error {
	vs.s.mu.Lock()
	defer vs.s.mu.Unlock()

	mm, ok := vs.s.mountMgrs[gameID]
	if !ok || !mm.IsMounted() {
		return fmt.Errorf("%w for %s", vfs.ErrNotMounted, gameID)
	}
	release, err := vs.s.reserveShared(gameID, dto.BusyOperationApply)
	if err != nil {
		return err
	}
	defer release()

	if vs.s.applyBusyLocked(gameID) {
		return &dto.GameRunningError{GameID: gameID, Operation: dto.GameRunningOperationApply}
	}

	gc, err := vs.s.config.EffectiveGameConfig(gameID)
	if err != nil {
		return err
	}
	ms := vs.s.mountStates[gameID]

	_, entries, err := vs.s.profileMgr.Load(gameID, ms.profileName)
	if err != nil {
		return err
	}

	layers := vs.buildLayers(gameID, gc, entries)
	oldLayers := mm.AppliedLayers()
	rootManager, err := vs.s.ensureRootDeploymentManager(gameID, gc)
	if err != nil {
		return err
	}
	if _, err := rootManager.Apply(layers, ms.profileName); err != nil {
		return fmt.Errorf("applying game-root deployment: %w", err)
	}
	if err := mm.MarkDirty(layers); err != nil {
		if _, restoreErr := rootManager.Apply(oldLayers, ms.profileName); restoreErr != nil {
			return fmt.Errorf("marking Data VFS dirty failed: %v; restoring game-root deployment also failed: %w", err, restoreErr)
		}
		return err
	}
	if err := mm.ReMaterialize(); err != nil {
		if _, restoreErr := rootManager.Apply(oldLayers, ms.profileName); restoreErr != nil {
			return fmt.Errorf("re-materializing Data VFS failed: %v; restoring game-root deployment also failed: %w", err, restoreErr)
		}
		return err
	}
	vs.s.publishGuarded(dto.StatusEventResult{VFSStatus: vs.vfsStatus(gameID, gc, ms.profileName, mm, entries)})
	return nil
}

func (vs *VFSService) buildLayers(gameID string, gc config.GameConfig, entries []mod.ModListEntry) []vfs.Layer {
	subpath := gc.DataSubpath
	if subpath == "" {
		subpath = "Data"
	}

	layers := []vfs.Layer{
		{Name: "__base__", RootPath: filepath.Join(gc.InstallPath, subpath), Enabled: true},
	}

	modsDir := config.ModsDir(gameID)
	guard := deployRootGuardFor(gameID)
	for _, e := range entries {
		if !e.Enabled {
			continue
		}
		if e.Name == profile.OverwriteModName {
			continue
		}
		if err := download.ValidateTargetModName(e.Name); err != nil {
			slog.Warn("skipping modlist entry with an invalid mod name", "game", gameID, "mod", e.Name, "err", err)
			continue
		}
		m := mod.NewMod(e.Name, gameID, filepath.Join(modsDir, e.Name))
		if guard != nil {
			if problem := guard(m.BasePath); problem != "" {
				slog.Warn("skipping mod with an invalid layout", "game", gameID, "mod", e.Name, "problem", problem)
				vs.s.emitInfo(fmt.Sprintf("[layout] skipped %s: %s; reinstall it", e.Name, problem))
				continue
			}
		}
		layers = append(layers, vfs.Layer{
			Name:     e.Name,
			RootPath: m.BasePath,
			Enabled:  true,
		})
	}

	owDir := filepath.Join(modsDir, profile.OverwriteModName)
	layers = append(layers, vfs.Layer{
		Name:     profile.OverwriteModName,
		RootPath: owDir,
		Enabled:  true,
	})
	return layers
}

// applyMountedRootDeployment re-applies gameID's game-root deployment under s.mu for a caller that holds no daemon lock, only while mm is still mounted, so no unmount can interleave.
func (s *session) applyMountedRootDeployment(gameID string, gc config.GameConfig, profileName string, mm *vfs.MountManager) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !mm.IsMounted() {
		return nil
	}
	return s.applyRootDeployment(gameID, gc, profileName)
}

// applyRootDeployment applies profileName's game-root deployment of gameID; the caller holds s.mu for writing.
func (s *session) applyRootDeployment(gameID string, gc config.GameConfig, profileName string) error {
	if profileName == "" {
		return nil
	}
	_, entries, err := s.profileMgr.Load(gameID, profileName)
	if err != nil {
		return err
	}
	manager, err := s.ensureRootDeploymentManager(gameID, gc)
	if err != nil {
		return err
	}
	_, err = manager.Apply(s.svc.vfs.buildLayers(gameID, gc, entries), profileName)
	return err
}

func (vs *VFSService) GetConflicts(gameID, profileName string) ([]dto.FileConflictResult, error) {
	gc, ok := vs.s.config.Games[gameID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}

	_, entries, err := vs.s.profileMgr.Load(gameID, profileName)
	if err != nil {
		return nil, err
	}

	layers := vs.buildLayers(gameID, gc, entries)
	cm, err := mod.BuildConflictMap(layers)
	if err != nil {
		return nil, err
	}

	var results []dto.FileConflictResult
	for _, c := range cm.Conflicts {
		results = append(results, dto.FileConflictResult{
			VirtualPath: c.VirtualPath,
			WinningMod:  c.Winner,
			LosingMods:  c.Losers,
		})
	}
	return results, nil
}

// sweepOrphanStageDirs removes the `.stage-<rand>/` and `.gorganizer-import-<uuid>/` staging folders of gameID that predate this daemon by stageSweepMargin, and the GUI's `.stage-ui-*` folders once older than guiStageRetention, never one an install may still be writing.
func (s *session) sweepOrphanStageDirs(gameID string) {
	cutoff := s.startedAt.Add(-stageSweepMargin)
	guiCutoff := s.startedAt.Add(-guiStageRetention)
	removed := 0
	for _, dir := range []string{config.ModsDir(gameID), config.ProfilesDir(gameID)} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if !strings.HasPrefix(e.Name(), ".stage-") && !strings.HasPrefix(e.Name(), mod.ImportStagePrefix) {
				continue
			}
			path := filepath.Join(dir, e.Name())
			info, err := e.Info()
			limit := cutoff
			if strings.HasPrefix(e.Name(), guiStagePrefix) {
				limit = guiCutoff
			}
			if err != nil || !info.ModTime().Before(limit) {
				continue
			}
			if err := os.RemoveAll(path); err != nil {
				slog.Warn("sweepOrphanStageDirs: remove failed", "path", path, "err", err)
				continue
			}
			removed++
		}
	}
	if removed > 0 {
		slog.Info("removed orphan install stage dirs", "game", gameID, "count", removed)
	}
}
