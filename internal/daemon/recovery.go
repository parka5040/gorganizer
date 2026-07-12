package daemon

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

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
	select {
	case <-s.recoveryReady:
		return nil
	default:
	}
	select {
	case <-s.recoveryReady:
		return nil
	case <-s.shutdownCh:
		return fmt.Errorf("daemon is shutting down")
	case <-time.After(recoveryWaitTimeout):
		return fmt.Errorf("still completing crash recovery — please try again in a moment")
	}
}

func (s *session) RecoverAll() {
	s.setReadinessStep("checking crash recovery", nil)
	defer s.setReadinessStep("recovery complete", func(r *dto.ReadinessResult) { r.RecoveryDone = true })
	defer s.signalRecoveryReady()

	seenRootManagers := make(map[*vfs.RootDeploymentManager]bool)
	for gameID := range s.config.Games {
		gameConfig, err := s.config.EffectiveGameConfig(gameID)
		if err != nil {
			slog.Warn("game-root recovery config unavailable", "game", gameID, "err", err)
			continue
		}
		rootManager, err := s.ensureRootDeploymentManager(gameID, gameConfig)
		if err != nil {
			slog.Warn("game-root recovery unavailable", "game", gameID, "err", err)
			continue
		}
		if seenRootManagers[rootManager] {
			continue
		}
		seenRootManagers[rootManager] = true
		outcome, err := rootManager.Recover()
		if err != nil {
			slog.Error("game-root crash recovery failed", "game", gameID, "err", err)
			continue
		}
		if outcome.Pending != nil {
			slog.Error("game-root recovery requires manual filesystem repair",
				"game", gameID, "path", outcome.Pending.Path, "reason", outcome.Pending.Reason)
			for affectedGameID, affectedManager := range s.rootDeployMgrs {
				if affectedManager != rootManager {
					continue
				}
				pending := &dto.RecoveryPendingResult{
					GameID: affectedGameID, DataPath: outcome.Pending.Path,
					BackupPath: filepath.Join(rootManager.GameRoot(), vfs.RootBackupDirName),
					Reason:     "game-root deployment: " + outcome.Pending.Reason,
				}
				s.pendingRecoveriesMu.Lock()
				s.rootPendingRecoveries[affectedGameID] = pending
				s.pendingRecoveriesMu.Unlock()
				select {
				case s.statusCh <- dto.StatusEventResult{RecoveryPending: pending}:
				default:
				}
			}
		}
	}

	pathToGames := map[string][]string{}
	pathOrder := []string{}
	for gameID, mm := range s.mountMgrs {
		dataPath := mm.DataPath()
		resolved, err := filepath.Abs(dataPath)
		if err != nil {
			resolved = dataPath
		}
		if _, seen := pathToGames[resolved]; !seen {
			pathOrder = append(pathOrder, resolved)
		}
		pathToGames[resolved] = append(pathToGames[resolved], gameID)
	}

	for _, dataPath := range pathOrder {
		gameIDs := pathToGames[dataPath]
		mm := s.mountMgrs[gameIDs[0]]
		outcome, err := mm.RecoverIfNeeded()
		if err != nil {
			slog.Error("crash recovery failed", "data_path", dataPath, "games", gameIDs, "err", err)
			continue
		}
		if outcome.Pending == nil {
			continue
		}
		pending := &dto.RecoveryPendingResult{
			GameID:     gameIDs[0],
			DataPath:   outcome.Pending.DataPath,
			BackupPath: outcome.Pending.BackupPath,
			Reason:     outcome.Pending.Reason,
		}
		s.pendingRecoveriesMu.Lock()
		s.pendingRecoveries[dataPath] = pending
		s.gamesAtPath[dataPath] = append([]string{}, gameIDs...)
		s.pendingRecoveriesMu.Unlock()
		slog.Warn("recovery pending — refusing to mount/launch until user confirms",
			"data_path", dataPath, "games", gameIDs, "reason", pending.Reason)
		select {
		case s.statusCh <- dto.StatusEventResult{RecoveryPending: pending}:
		default:
		}
	}
}

// recoveryPendingFor returns the pending recovery record for gameID, or nil.
func (s *session) recoveryPendingFor(gameID string) *dto.RecoveryPendingResult {
	s.pendingRecoveriesMu.Lock()
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

// findMutexConflict returns the gameID of the currently-mounted sibling in gameID's mutex group, or "".
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
