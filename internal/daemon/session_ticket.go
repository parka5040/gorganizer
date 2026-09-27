package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/vfs"
)

const sessionTicketSuffix = vfs.RetainedSessionSiblingSuffix
const sessionTicketVersion = 1

type launchTicket struct {
	SchemaVersion int       `json:"schema_version"`
	GameID        string    `json:"game_id"`
	Profile       string    `json:"profile"`
	LaunchedAt    time.Time `json:"launched_at"`
	AppIDs        []int     `json:"app_ids"`
	PID           int       `json:"pid"`
}

type deferredRecovery struct {
	gameIDs    []string
	reason     string
	detectedAt time.Time
}

type installRecoveryUnit struct {
	key       string
	gameIDs   []string
	appIDs    []int
	dataPaths []string
}

type heldLanding struct {
	snap        download.DownloadSnapshot
	path        string
	sidecar     download.ArchiveSidecar
	autoInstall bool
}

// writeLaunchTicket durably records a launch before handing it to Steam or the script extender.
func (s *session) writeLaunchTicket(gameID, profileName, dataPath string, appIDs []int) error {
	ticket := launchTicket{
		SchemaVersion: sessionTicketVersion, GameID: gameID, Profile: profileName,
		LaunchedAt: s.clock().UTC(), AppIDs: appIDs, PID: 0,
	}
	data, err := json.Marshal(ticket)
	if err != nil {
		return fmt.Errorf("preparing the game launch record: %w", err)
	}
	outcome, err := atomicfile.WriteFileDurable(dataPath+sessionTicketSuffix, append(data, '\n'), 0o600)
	if err != nil {
		return fmt.Errorf("could not save the game launch record; the game was not started: %w", err)
	}
	if outcome != atomicfile.Durable {
		return fmt.Errorf("could not safely save the game launch record; the game was not started")
	}
	return nil
}

// writeLaunchTicketForGame records the configured Steam app IDs of every game sharing this physical install before launch.
func (s *session) writeLaunchTicketForGame(gameID, profileName, dataPath string) error {
	s.mu.RLock()
	key := s.fenceKeyLocked(gameID)
	appIDs := s.steamAppIDsLocked(s.gamesOnFenceKeyLocked(gameID, key))
	s.mu.RUnlock()
	return s.writeLaunchTicket(gameID, profileName, dataPath, appIDs)
}

// removeLaunchTicket removes the durable launch record after the deploy folder has been restored.
func removeLaunchTicket(dataPath string) error {
	if err := atomicfile.RemoveDurable(dataPath + sessionTicketSuffix); err != nil {
		return fmt.Errorf("removing the game launch record: %w", err)
	}
	return nil
}

// installRecoveryUnits snapshots the configured games, app IDs and deploy folders of each physical install.
func (s *session) installRecoveryUnits() map[string]*installRecoveryUnit {
	s.mu.RLock()
	units := make(map[string]*installRecoveryUnit)
	for gameID, gc := range s.config.Games {
		key := s.fenceKeyLocked(gameID)
		unit := units[key]
		if unit == nil {
			unit = &installRecoveryUnit{key: key}
			units[key] = unit
		}
		unit.gameIDs = append(unit.gameIDs, gameID)
		effective, err := s.config.EffectiveGameConfig(gameID)
		if err != nil {
			slog.Warn("cannot resolve game for startup recovery", "game", gameID, "err", err)
			continue
		}
		if effective.SteamAppID > 0 {
			unit.appIDs = append(unit.appIDs, effective.SteamAppID)
		}
		subpath := effective.DataSubpath
		if subpath == "" {
			subpath = "Data"
		}
		if root := s.mountInstallPath(gc); root != "" {
			unit.dataPaths = append(unit.dataPaths, filepath.Join(root, subpath))
		}
	}
	s.mu.RUnlock()
	for _, unit := range units {
		sort.Strings(unit.gameIDs)
	}
	return units
}

// recoveryIdle checks the process table and launch tickets of an install without holding the daemon lock.
func (s *session) recoveryIdle(unit *installRecoveryUnit) string {
	if !filepath.IsAbs(unit.key) {
		return ""
	}
	running, err := s.processRunningIn(unit.key, unit.appIDs)
	reason := ""
	switch {
	case err != nil:
		reason = "the game process check failed: " + err.Error()
	case running:
		reason = "a game process may still be running"
	default:
		for _, dataPath := range unit.dataPaths {
			data, readErr := os.ReadFile(dataPath + sessionTicketSuffix)
			if errors.Is(readErr, fs.ErrNotExist) {
				continue
			}
			if readErr != nil {
				reason = "the game launch record could not be read: " + readErr.Error()
				break
			}
			var ticket launchTicket
			if json.Unmarshal(data, &ticket) != nil || ticket.SchemaVersion != sessionTicketVersion || ticket.LaunchedAt.IsZero() {
				reason = "the game launch record is invalid"
				break
			}
			if s.clock().Sub(ticket.LaunchedAt) < steamLaunchGrace {
				reason = "the game was launched less than two minutes ago"
				break
			}
		}
	}
	return reason
}

// classifyStartupRecoveries records physical installs whose game or fresh launch ticket may still be running before constructor recovery touches any mod files.
func (s *session) classifyStartupRecoveries() {
	for _, unit := range s.installRecoveryUnits() {
		reason := s.recoveryIdle(unit)
		if reason == "" {
			continue
		}
		s.pendingRecoveriesMu.Lock()
		s.deferredRecoveries[unit.key] = deferredRecovery{
			gameIDs: append([]string(nil), unit.gameIDs...), reason: reason, detectedAt: s.clock(),
		}
		s.pendingRecoveriesMu.Unlock()
		slog.Warn("startup recovery deferred; leaving game mods and deployment untouched", "games", unit.gameIDs, "install", unit.key, "reason", reason)
	}
}

// deferredForLocked refuses an operation on an install whose startup recovery was deferred; the caller holds s.mu.
func (s *session) deferredForLocked(gameID, operation string) error {
	key := s.fenceKeyLocked(gameID)
	s.pendingRecoveriesMu.Lock()
	_, deferred := s.deferredRecoveries[key]
	s.pendingRecoveriesMu.Unlock()
	if deferred {
		return &dto.RecoveryDeferredError{GameID: gameID, Operation: operation}
	}
	return nil
}

// deferredFor refuses an operation on an install whose startup recovery was deferred.
func (s *session) deferredFor(gameID, operation string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.deferredForLocked(gameID, operation)
}

// holdDeferredLanding saves a landed archive until its install's deferred or pending recovery has finished.
func (s *session) holdDeferredLanding(landing heldLanding) bool {
	s.mu.RLock()
	key := s.fenceKeyLocked(landing.snap.GameID)
	s.mu.RUnlock()
	s.pendingRecoveriesMu.Lock()
	defer s.pendingRecoveriesMu.Unlock()
	_, deferred := s.deferredRecoveries[key]
	if !deferred && !s.replayPending[key] && !s.replayRunning[key] {
		return false
	}
	for i, held := range s.heldLandings[key] {
		if held.snap.GameID == landing.snap.GameID && held.path == landing.path {
			s.heldLandings[key][i] = landing
			return true
		}
	}
	s.heldLandings[key] = append(s.heldLandings[key], landing)
	return true
}

// RetryDeferredRecovery attempts to finish one install's deferred recovery when its processes and launch tickets are no longer active.
func (s *session) RetryDeferredRecovery(gameID string) error {
	if err := s.awaitRecovery(); err != nil {
		return err
	}
	s.mu.RLock()
	key := s.fenceKeyLocked(gameID)
	s.mu.RUnlock()
	s.pendingRecoveriesMu.Lock()
	_, deferred := s.deferredRecoveries[key]
	s.pendingRecoveriesMu.Unlock()
	if !deferred {
		return nil
	}
	release, err := s.acquireRecoveryExclusive(gameID)
	if err != nil {
		return err
	}
	defer release()
	s.pendingRecoveriesMu.Lock()
	_, deferred = s.deferredRecoveries[key]
	s.pendingRecoveriesMu.Unlock()
	if !deferred {
		return nil
	}
	units := s.installRecoveryUnits()
	unit := units[key]
	if unit == nil {
		return fmt.Errorf("recovery install %s is no longer configured", gameID)
	}
	if reason := s.recoveryIdle(unit); reason != "" {
		return &dto.RecoveryDeferredError{GameID: gameID, Operation: "recovery"}
	}
	if err := s.refuseWhenShuttingDown("recovery"); err != nil {
		return err
	}
	for _, id := range unit.gameIDs {
		recoverReinstalls(config.ModsDir(id))
	}
	if !s.recoverUnits(unit.gameIDs) {
		return fmt.Errorf("recovery of %s could not finish; it will be retried", gameID)
	}
	if err := s.refuseWhenShuttingDown("recovery"); err != nil {
		return err
	}
	for _, id := range unit.gameIDs {
		s.sweepOrphanStageDirs(id)
	}
	s.pendingRecoveriesMu.Lock()
	delete(s.deferredRecoveries, key)
	s.replayPending[key] = true
	s.pendingRecoveriesMu.Unlock()
	for _, id := range unit.gameIDs {
		status, statusErr := s.svc.vfs.GetVFSStatus(id)
		if statusErr == nil {
			s.publishGuarded(dto.StatusEventResult{VFSStatus: status})
		}
		s.mu.RLock()
		pending := s.recoveryPendingFor(id)
		s.mu.RUnlock()
		if pending != nil {
			s.publishRecoveryEvent(dto.StatusEventResult{RecoveryPending: pending})
		}
	}
	release()
	s.replayDeferredLandings(gameID)
	return nil
}

// replayDeferredLandings resumes held archives and interrupted dependency requests after every game sharing the recovered install is clear of pending recovery.
func (s *session) replayDeferredLandings(gameID string) {
	if s.shuttingDown.Load() {
		return
	}
	s.mu.RLock()
	key := s.fenceKeyLocked(gameID)
	games := s.gamesOnFenceKeyLocked(gameID, key)
	for _, id := range games {
		if s.recoveryPendingFor(id) != nil {
			s.mu.RUnlock()
			return
		}
	}
	s.mu.RUnlock()
	s.pendingRecoveriesMu.Lock()
	if !s.replayPending[key] || s.replayRunning[key] {
		s.pendingRecoveriesMu.Unlock()
		return
	}
	s.replayRunning[key] = true
	s.pendingRecoveriesMu.Unlock()
	var resumed []recoveredLanding
	if s.svc.modDeps != nil {
		resumed = s.svc.modDeps.recoverInterruptedRequests(games)
	}
	s.pendingRecoveriesMu.Lock()
	delete(s.replayPending, key)
	delete(s.replayRunning, key)
	landings := s.heldLandings[key]
	delete(s.heldLandings, key)
	s.pendingRecoveriesMu.Unlock()
	priorInstall := map[string]bool{}
	for _, landing := range resumed {
		if landing.install != "" {
			priorInstall[landing.gameID+"\x00"+landing.path] = true
		}
	}
	heldByPath := map[string]bool{}
	var replay []heldLanding
	for _, landing := range landings {
		key := landing.snap.GameID + "\x00" + landing.path
		if !priorInstall[key] {
			replay = append(replay, landing)
			heldByPath[key] = true
		}
	}
	if len(replay) != 0 {
		s.goBackground("replay deferred landings", func() {
			for _, landing := range replay {
				if s.shuttingDown.Load() {
					return
				}
				s.svc.archives.handleLandedArchiveMode(landing.snap, landing.path, landing.sidecar, landing.autoInstall)
			}
		})
	}
	if s.svc.modDeps != nil {
		var remaining []recoveredLanding
		for _, landing := range resumed {
			if landing.install != "" || !heldByPath[landing.gameID+"\x00"+landing.path] {
				remaining = append(remaining, landing)
			}
		}
		s.svc.modDeps.resumeRecoveredLandings(remaining)
	}
}

// retryDeferredRecoveriesLoop retries deferred installs every thirty seconds until shutdown begins.
func (s *session) retryDeferredRecoveriesLoop() {
	if err := s.awaitRecovery(); err != nil {
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.shutdownCh:
			return
		case <-ticker.C:
			s.retryDeferredRecoveriesTick()
		}
	}
}

// retryDeferredRecoveriesTick attempts each deferred physical install once without blocking another install's recovery.
func (s *session) retryDeferredRecoveriesTick() {
	s.pendingRecoveriesMu.Lock()
	var games []string
	for _, deferred := range s.deferredRecoveries {
		if len(deferred.gameIDs) != 0 {
			games = append(games, deferred.gameIDs[0])
		}
	}
	s.pendingRecoveriesMu.Unlock()
	sort.Strings(games)
	for _, id := range games {
		if s.shuttingDown.Load() {
			return
		}
		if err := s.RetryDeferredRecovery(id); err != nil {
			var deferred *dto.RecoveryDeferredError
			var busy *dto.OperationBusyError
			if !errors.As(err, &deferred) && !errors.As(err, &busy) {
				slog.Warn("retrying deferred recovery failed", "game", id, "err", err)
			}
		}
	}
}

// recoverableGameIDs returns the configured games whose installs are not awaiting deferred startup recovery.
func (s *session) recoverableGameIDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var ids []string
	for gameID := range s.config.Games {
		if s.deferredForLocked(gameID, "recovery") == nil {
			ids = append(ids, gameID)
		}
	}
	sort.Strings(ids)
	return ids
}
