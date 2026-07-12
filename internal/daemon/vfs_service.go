package daemon

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/mod"
	"github.com/parka/gorganizer/internal/profile"
	"github.com/parka/gorganizer/internal/vfs"
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

	if conflict := vs.s.findMutexConflict(gameID); conflict != "" {
		if !autoSwap {
			return nil, &VFSMutexError{
				GameID:      gameID,
				Conflicting: conflict,
				Group:       mutexGroupOf(gameID),
			}
		}
		if conflictMM, ok := vs.s.mountMgrs[conflict]; ok && conflictMM.IsMounted() {
			if vs.s.mountBusy(conflict) {
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
			select {
			case vs.s.statusCh <- dto.StatusEventResult{VFSStatus: &dto.VFSStatusResult{GameID: conflict}}:
			default:
			}
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

	select {
	case vs.s.statusCh <- dto.StatusEventResult{VFSStatus: st}:
	default:
	}
	return st, nil
}

func (vs *VFSService) UnmountVFS(gameID string) error {
	vs.s.mu.Lock()
	defer vs.s.mu.Unlock()

	mm, ok := vs.s.mountMgrs[gameID]
	if !ok {
		return fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
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

	select {
	case vs.s.statusCh <- dto.StatusEventResult{VFSStatus: &dto.VFSStatusResult{GameID: gameID}}:
	default:
	}
	return nil
}

func (vs *VFSService) GetVFSStatus(gameID string) (*dto.VFSStatusResult, error) {
	vs.s.mu.RLock()
	defer vs.s.mu.RUnlock()

	mm, ok := vs.s.mountMgrs[gameID]
	if !ok {
		return &dto.VFSStatusResult{GameID: gameID}, nil
	}

	st := &dto.VFSStatusResult{
		Mounted: mm.IsMounted(),
		GameID:  gameID,
	}

	if ms, ok := vs.s.mountStates[gameID]; ok {
		st.ProfileName = ms.profileName
	}
	if mm.IsMounted() && mm.Tree() != nil {
		fileCount, _ := mm.Tree().Stats()
		st.TotalFileCount = fileCount
	}
	return st, nil
}

func (vs *VFSService) RestoreFromBackup(gameID string) error {
	vs.s.pendingRecoveriesMu.Lock()
	rootPending := vs.s.rootPendingRecoveries[gameID]
	vs.s.pendingRecoveriesMu.Unlock()
	if rootPending != nil {
		manager, ok := vs.s.rootDeployMgrs[gameID]
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
		vs.s.pendingRecoveriesMu.Lock()
		for affectedGameID, affectedManager := range vs.s.rootDeployMgrs {
			if affectedManager == manager {
				delete(vs.s.rootPendingRecoveries, affectedGameID)
			}
		}
		vs.s.pendingRecoveriesMu.Unlock()
		return nil
	}
	mm, ok := vs.s.mountMgrs[gameID]
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
		select {
		case vs.s.statusCh <- dto.StatusEventResult{Info: fmt.Sprintf("recovery resolved for %s", sibling)}:
		default:
		}
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

	if vs.s.mountBusy(gameID) {
		return fmt.Errorf("cannot apply changes while %s is running — stop it first", gameID)
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
	select {
	case vs.s.statusCh <- dto.StatusEventResult{VFSStatus: vs.vfsStatus(gameID, gc, ms.profileName, mm, entries)}:
	default:
	}
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
	for _, e := range entries {
		if !e.Enabled {
			continue
		}
		if e.Name == profile.OverwriteModName {
			continue
		}
		m := mod.NewMod(e.Name, gameID, filepath.Join(modsDir, e.Name))
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

// sweepOrphanStageDirs removes any `.stage-<rand>/` or `.gorganizer-import-<uuid>/` staging leftovers.
func (vs *VFSService) sweepOrphanStageDirs(gameID string) {
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
