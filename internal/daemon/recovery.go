package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/vfs"
)

const recoveryWaitTimeout = 60 * time.Second

var mutexGroups = map[string]string{
	"falloutnv": "fnv-data",
	"ttw":       "fnv-data",
}

// mutexGroupOf returns the mutex group for a gameID, "" if none.
func mutexGroupOf(gameID string) string {
	return mutexGroups[gameID]
}

// signalRecoveryReady unblocks awaitRecovery; safe to call more than once.
func (s *session) signalRecoveryReady() {
	s.recoveryReadyOnce.Do(func() { close(s.recoveryReady) })
}

// awaitRecovery blocks until crash recovery has completed, shutdown, or timeout.
func (s *session) awaitRecovery() error {
	return s.awaitRecoveryCtx(context.Background())
}

// awaitRecoveryCtx blocks until crash recovery has completed, shutdown, timeout, or the caller's context ends, returning the context's error in the last case.
func (s *session) awaitRecoveryCtx(ctx context.Context) error {
	select {
	case <-s.recoveryReady:
		return nil
	default:
	}
	timer := time.NewTimer(recoveryWaitTimeout)
	defer timer.Stop()
	select {
	case <-s.recoveryReady:
		return nil
	case <-s.shutdownCh:
		return &dto.ShuttingDownError{}
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("still completing crash recovery — please try again in a moment")
	}
}

// RecoverAll resolves interrupted transactions and farms, then reaps orphan staging, mod trash, and archive extractions before admitting work.
func (s *session) RecoverAll() {
	s.setReadinessStep("checking crash recovery", nil)
	defer s.setReadinessStep("recovery complete", func(r *dto.ReadinessResult) { r.RecoveryDone = true })
	defer s.signalRecoveryReady()

	ids := s.recoverableGameIDs()
	s.recoverUnits(ids)
	for _, gameID := range ids {
		s.sweepOrphanStageDirs(gameID)
	}
	for _, gameID := range s.configuredGameIDs() {
		s.sweepUninstalledModTrash(gameID)
	}
	s.sweepStaleExtractions()
}

// sweepUninstalledModTrash removes direct, non-symlinked trash directories left by interrupted uninstalls.
func (s *session) sweepUninstalledModTrash(gameID string) {
	modsDir := config.ModsDir(gameID)
	entries, err := os.ReadDir(modsDir)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("could not scan uninstalled mod trash", "game", gameID, "err", err)
		}
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), ".gorganizer-trash-") {
			continue
		}
		path := filepath.Join(modsDir, entry.Name())
		if err := removeModFolder(path); err != nil {
			slog.Warn("could not remove uninstalled mod trash", "game", gameID, "path", path, "err", err)
		}
	}
}

// recoverUnits runs the startup recovery sequence for the selected games, returning false if an interrupted transaction still needs another attempt.
func (s *session) recoverUnits(ids []string) bool {
	deferredLoaders := s.recoverLoaderGames(ids, false)
	rootOK := s.recoverRootDeployments(ids)
	dataOK := s.recoverDataFarms(ids)
	heldLoaders := s.retryDeferredLoaderRecovery(deferredLoaders)
	orphanOK := s.deactivateOrphanedRootDeployments(ids, heldLoaders)
	return rootOK && dataOK && orphanOK && len(heldLoaders) == 0
}

// configuredGameIDs returns the configured game IDs, sorted, read under s.mu.
func (s *session) configuredGameIDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.config.Games))
	for gameID := range s.config.Games {
		ids = append(ids, gameID)
	}
	sort.Strings(ids)
	return ids
}

type rootRecoveryUnit struct {
	manager *vfs.RootDeploymentManager
	gameIDs []string
}

// identifiedRecovery retains an unchanged pending item's identity and assigns a random identity to a replacement.
func identifiedRecovery(previous, pending *dto.RecoveryPendingResult) *dto.RecoveryPendingResult {
	if previous != nil && previous.RecoveryID != "" && previous.GameID == pending.GameID && previous.DataPath == pending.DataPath &&
		previous.BackupPath == pending.BackupPath && previous.Reason == pending.Reason && previous.Kind == pending.Kind {
		return previous
	}
	pending.RecoveryID = ""
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		slog.Error("cannot generate a recovery identity", "game", pending.GameID, "err", err)
		return pending
	}
	pending.RecoveryID = hex.EncodeToString(bytes[:])
	return pending
}

// rootRecoveryUnits resolves the distinct root deployment managers of the selected games with the games sharing each.
func (s *session) rootRecoveryUnits(gameIDs []string) []rootRecoveryUnit {
	s.mu.Lock()
	defer s.mu.Unlock()
	var units []rootRecoveryUnit
	index := map[*vfs.RootDeploymentManager]int{}
	ids := append([]string(nil), gameIDs...)
	sort.Strings(ids)
	for _, gameID := range ids {
		gameConfig, err := s.config.EffectiveGameConfig(gameID)
		if err != nil {
			slog.Warn("game-root recovery config unavailable", "game", gameID, "err", err)
			continue
		}
		manager, err := s.ensureRootDeploymentManager(gameID, gameConfig)
		if err != nil {
			slog.Warn("game-root recovery unavailable", "game", gameID, "err", err)
			continue
		}
		if _, seen := index[manager]; !seen {
			index[manager] = len(units)
			units = append(units, rootRecoveryUnit{manager: manager})
		}
	}
	for gameID, manager := range s.rootDeployMgrs {
		if i, ok := index[manager]; ok {
			units[i].gameIDs = append(units[i].gameIDs, gameID)
		}
	}
	for i := range units {
		sort.Strings(units[i].gameIDs)
	}
	return units
}

// recoverRootDeployments finishes interrupted game-root transactions for selected games and registers pending recoveries for drift.
func (s *session) recoverRootDeployments(ids []string) bool {
	complete := true
	for _, unit := range s.rootRecoveryUnits(ids) {
		if len(unit.gameIDs) == 0 {
			continue
		}
		outcome, err := unit.manager.Recover()
		if err != nil {
			slog.Error("game-root crash recovery failed", "games", unit.gameIDs, "err", err)
			complete = false
			continue
		}
		if outcome.Pending == nil {
			continue
		}
		slog.Error("game-root recovery requires manual filesystem repair",
			"games", unit.gameIDs, "path", outcome.Pending.Path, "reason", outcome.Pending.Reason)
		for _, affectedGameID := range unit.gameIDs {
			pending := &dto.RecoveryPendingResult{
				GameID: affectedGameID, DataPath: outcome.Pending.Path,
				BackupPath: filepath.Join(unit.manager.GameRoot(), vfs.RootBackupDirName),
				Reason:     "game-root deployment: " + outcome.Pending.Reason,
				Kind:       dto.RecoveryKindGameRoot,
			}
			s.pendingRecoveriesMu.Lock()
			pending = identifiedRecovery(s.rootPendingRecoveries[affectedGameID], pending)
			s.rootPendingRecoveries[affectedGameID] = pending
			s.pendingRecoveriesMu.Unlock()
			s.publishRecoveryEvent(dto.StatusEventResult{RecoveryPending: pending})
			s.publishRecoveryStatuses(affectedGameID)
		}
	}
	return complete
}

// recoverDataFarms heals each selected Data path once outside s.mu and registers a pending recovery for ambiguous state.
func (s *session) recoverDataFarms(gameIDs []string) bool {
	s.mu.RLock()
	pathToGames := map[string][]string{}
	managers := map[string]*vfs.MountManager{}
	pathOrder := []string{}
	ids := append([]string(nil), gameIDs...)
	sort.Strings(ids)
	for _, gameID := range ids {
		mm := s.mountMgrs[gameID]
		if mm == nil {
			continue
		}
		dataPath := mm.DataPath()
		resolved, err := filepath.Abs(dataPath)
		if err != nil {
			resolved = dataPath
		}
		if _, seen := pathToGames[resolved]; !seen {
			pathOrder = append(pathOrder, resolved)
			managers[resolved] = mm
		}
		pathToGames[resolved] = append(pathToGames[resolved], gameID)
	}
	s.mu.RUnlock()

	complete := true
	for _, dataPath := range pathOrder {
		gameIDs := pathToGames[dataPath]
		outcome, err := managers[dataPath].RecoverIfNeeded()
		if err != nil {
			slog.Error("crash recovery failed", "data_path", dataPath, "games", gameIDs, "err", err)
			complete = false
			continue
		}
		if outcome.Restored {
			if err := removeLaunchTicket(dataPath); err != nil {
				slog.Warn("removing launch record after farm recovery failed", "games", gameIDs, "err", err)
			}
		}
		if outcome.Pending == nil {
			continue
		}
		pending := &dto.RecoveryPendingResult{
			GameID:     gameIDs[0],
			DataPath:   outcome.Pending.DataPath,
			BackupPath: outcome.Pending.BackupPath,
			Reason:     outcome.Pending.Reason,
			Kind:       dto.RecoveryKindData,
		}
		s.pendingRecoveriesMu.Lock()
		pending = identifiedRecovery(s.pendingRecoveries[dataPath], pending)
		s.pendingRecoveries[dataPath] = pending
		s.gamesAtPath[dataPath] = append([]string{}, gameIDs...)
		s.pendingRecoveriesMu.Unlock()
		slog.Warn("recovery pending — refusing to mount/launch until user confirms",
			"data_path", dataPath, "games", gameIDs, "reason", pending.Reason)
		s.publishRecoveryEvent(dto.StatusEventResult{RecoveryPending: pending})
		s.publishRecoveryStatuses(gameIDs[0])
	}
	return complete
}

// deactivateOrphanedRootDeployments removes orphaned root deployments of selected games when no farm, pending recovery or loader transaction still uses them.
func (s *session) deactivateOrphanedRootDeployments(ids []string, heldLoaderDirs map[string]bool) bool {
	complete := true
	for _, unit := range s.rootRecoveryUnits(ids) {
		if len(unit.gameIDs) == 0 {
			continue
		}
		if s.rootDeploymentInUse(unit.gameIDs) {
			continue
		}
		if heldLoaderDirs[filepath.Clean(unit.manager.GameRoot())] {
			slog.Warn("keeping a game-root deployment while another mod-loader transaction holds its install", "games", unit.gameIDs)
			continue
		}
		manifest, err := unit.manager.ActiveManifest()
		if err != nil {
			slog.Warn("checking a game-root deployment after recovery failed", "games", unit.gameIDs, "err", err)
			complete = false
			continue
		}
		if manifest == nil {
			continue
		}
		stats, err := unit.manager.Deactivate()
		if err != nil {
			slog.Error("deactivating a game-root deployment left by an unmounted farm failed; loader changes stay refused until it is removed",
				"games", unit.gameIDs, "err", err)
			complete = false
			continue
		}
		slog.Warn("deactivated a game-root deployment left by an unmounted farm", "games", unit.gameIDs,
			"links_removed", stats.LinksRemoved, "backups_restored", stats.BackupsRestored)
	}
	return complete
}

// rootDeploymentInUse reports whether any of gameIDs is mounted or has a pending recovery, so its root deployment must stay.
func (s *session) rootDeploymentInUse(gameIDs []string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, gameID := range gameIDs {
		if mm, ok := s.mountMgrs[gameID]; ok && mm.IsMounted() {
			return true
		}
		if s.recoveryPendingFor(gameID) != nil {
			return true
		}
	}
	return false
}

// recoveryPendingFor returns the pending recovery record for gameID, or nil; the caller holds s.mu for reading or writing.
func (s *session) recoveryPendingFor(gameID string) *dto.RecoveryPendingResult {
	s.pendingRecoveriesMu.Lock()
	if pending := s.loaderPendingRecoveries[gameID]; pending != nil {
		s.pendingRecoveriesMu.Unlock()
		return pending
	}
	if pending := s.rootPendingRecoveries[gameID]; pending != nil {
		s.pendingRecoveriesMu.Unlock()
		return pending
	}
	s.pendingRecoveriesMu.Unlock()
	mm, ok := s.mountMgrs[gameID]
	if !ok {
		return nil
	}
	resolved, err := filepath.Abs(mm.DataPath())
	if err != nil {
		resolved = mm.DataPath()
	}
	s.pendingRecoveriesMu.Lock()
	defer s.pendingRecoveriesMu.Unlock()
	return s.pendingRecoveries[resolved]
}

// findMutexConflict returns the gameID of the currently-mounted sibling in gameID's mutex group, or ""; the caller holds s.mu for reading or writing.
func (s *session) findMutexConflict(gameID string) string {
	group := mutexGroupOf(gameID)
	if group == "" {
		return ""
	}
	for other, otherGroup := range mutexGroups {
		if other == gameID || otherGroup != group {
			continue
		}
		mm, ok := s.mountMgrs[other]
		if !ok || !mm.IsMounted() {
			continue
		}
		return other
	}
	return ""
}
