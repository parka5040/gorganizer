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

// classifyStartupRecoveries records physical installs whose game or fresh launch ticket may still be running before constructor recovery touches any mod files.
func (s *session) classifyStartupRecoveries() {
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
		if !filepath.IsAbs(unit.key) {
			continue
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
