package daemon

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/parka/gorganizer/internal/dto"
)

const (
	fenceUnconfiguredKey = "game:"
	steamLaunchGrace     = 2 * time.Minute
)

type fenceHolder struct {
	op     string
	gameID string
}

type sharedReservation struct {
	id      uint64
	release func()
}

// Release drops a shared reservation once.
func (r sharedReservation) Release() {
	r.release()
}

// fenceKeyLocked returns the cleaned physical install root of gameID, or a game-ID key when it has none; the caller holds s.mu.
func (s *session) fenceKeyLocked(gameID string) string {
	if s.config != nil {
		if gc, ok := s.config.Games[gameID]; ok {
			if root := s.mountInstallPath(gc); root != "" {
				return filepath.Clean(root)
			}
		}
	}
	return fenceUnconfiguredKey + gameID
}

// gamesOnFenceKeyLocked returns gameID followed by every other configured game that shares its fence key; the caller holds s.mu.
func (s *session) gamesOnFenceKeyLocked(gameID, key string) []string {
	games := []string{gameID}
	if s.config == nil {
		return games
	}
	var others []string
	for id := range s.config.Games {
		if id != gameID && s.fenceKeyLocked(id) == key {
			others = append(others, id)
		}
	}
	sort.Strings(others)
	return append(games, others...)
}

// reserveShared takes a non-blocking shared reservation for op on gameID's install root; the caller holds s.mu.
func (s *session) reserveShared(gameID, op string) (func(), error) {
	reservation, err := s.reserveSharedOwned(gameID, op)
	if err != nil {
		return nil, err
	}
	return reservation.Release, nil
}

// reserveSharedOwned returns an identifiable shared reservation for op on gameID's install root; the caller holds s.mu.
func (s *session) reserveSharedOwned(gameID, op string) (sharedReservation, error) {
	if err := s.refuseWhenShuttingDown(op); err != nil {
		return sharedReservation{}, err
	}
	key := s.fenceKeyLocked(gameID)
	holder := fenceHolder{op: op, gameID: gameID}
	s.fenceMu.Lock()
	defer s.fenceMu.Unlock()
	if exclusive, ok := s.fenceExclusive[key]; ok {
		return sharedReservation{}, &dto.OperationBusyError{GameID: gameID, Operation: exclusive.op, Holder: exclusive.gameID}
	}
	if s.fenceShared == nil {
		s.fenceShared = make(map[string]map[uint64]fenceHolder)
	}
	holders := s.fenceShared[key]
	if holders == nil {
		holders = make(map[uint64]fenceHolder)
		s.fenceShared[key] = holders
	}
	s.nextFenceID++
	id := s.nextFenceID
	holders[id] = holder
	var once sync.Once
	return sharedReservation{id: id, release: func() { once.Do(func() { s.releaseShared(key, id) }) }}, nil
}

// acquireShared takes a shared reservation for a caller that holds no daemon lock.
func (s *session) acquireShared(gameID, op string) (func(), error) {
	reservation, err := s.acquireSharedOwned(gameID, op)
	if err != nil {
		return nil, err
	}
	return reservation.Release, nil
}

// acquireSharedOwned returns an identifiable shared reservation for a caller that holds no daemon lock.
func (s *session) acquireSharedOwned(gameID, op string) (sharedReservation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.reserveSharedOwned(gameID, op)
}

// releaseShared drops one shared reservation by id on key.
func (s *session) releaseShared(key string, id uint64) {
	s.fenceMu.Lock()
	defer s.fenceMu.Unlock()
	holders := s.fenceShared[key]
	delete(holders, id)
	if len(holders) == 0 {
		delete(s.fenceShared, key)
	}
}

// pendingAdmissionLocked reports a launch, tool, or script-extender reservation on gameID's install other than owner; the caller holds s.mu.
func (s *session) pendingAdmissionLocked(gameID string, owner uint64) *dto.OperationBusyError {
	key := s.fenceKeyLocked(gameID)
	s.fenceMu.Lock()
	defer s.fenceMu.Unlock()
	var first fenceHolder
	var firstID uint64
	for id, holder := range s.fenceShared[key] {
		if id == owner || holder.op != dto.BusyOperationLaunch && holder.op != dto.BusyOperationTool && holder.op != dto.BusyOperationScriptExtender {
			continue
		}
		if firstID == 0 || id < firstID {
			firstID, first = id, holder
		}
	}
	if firstID == 0 {
		return nil
	}
	return &dto.OperationBusyError{GameID: gameID, Operation: first.op, Holder: first.gameID}
}

// reserveExclusiveLocked takes the exclusive reservation for op once the daemon is not shutting down and no game on gameID's install root is pending recovery, mounted, running, root-deployed, or reserved; the caller holds s.mu for writing.
func (s *session) reserveExclusiveLocked(gameID, op string) (func(), error) {
	if err := s.refuseWhenShuttingDown(op); err != nil {
		return nil, err
	}
	key := s.fenceKeyLocked(gameID)
	games := s.gamesOnFenceKeyLocked(gameID, key)
	for _, id := range games {
		if pending := s.recoveryPendingFor(id); pending != nil {
			return nil, fmt.Errorf("recovery pending for %s: %s — confirm via the GUI prompt or `gorganizerctl recover-confirm` first",
				id, pending.Reason)
		}
		if mm, ok := s.mountMgrs[id]; ok && mm.IsMounted() {
			return nil, &dto.OperationBusyError{GameID: gameID, Operation: dto.BusyOperationMounted, Holder: id}
		}
		if s.trackedMountBusy(id) {
			return nil, &dto.OperationBusyError{GameID: gameID, Operation: dto.BusyOperationRunning, Holder: id}
		}
		if err := s.requireRootDeploymentIdleLocked(gameID, id); err != nil {
			return nil, err
		}
	}
	if err := s.requireInstallIdleLocked(gameID, key, games); err != nil {
		return nil, err
	}
	holder := fenceHolder{op: op, gameID: gameID}
	s.fenceMu.Lock()
	defer s.fenceMu.Unlock()
	if exclusive, ok := s.fenceExclusive[key]; ok {
		return nil, &dto.OperationBusyError{GameID: gameID, Operation: exclusive.op, Holder: exclusive.gameID}
	}
	if holders := s.fenceShared[key]; len(holders) > 0 {
		first := firstFenceHolder(holders)
		return nil, &dto.OperationBusyError{GameID: gameID, Operation: first.op, Holder: first.gameID}
	}
	if s.fenceExclusive == nil {
		s.fenceExclusive = make(map[string]fenceHolder)
	}
	s.fenceExclusive[key] = holder
	var once sync.Once
	return func() { once.Do(func() { s.releaseExclusive(key, holder) }) }, nil
}

// requireInstallIdleLocked refuses while a process runs from the install root key, or a Steam launch on it is younger than steamLaunchGrace, and clears older Steam-launch flags once no such process exists; the caller holds s.mu.
func (s *session) requireInstallIdleLocked(gameID, key string, games []string) error {
	if holder := s.runningHolderLocked(gameID, key, games); holder != "" {
		return &dto.OperationBusyError{GameID: gameID, Operation: dto.BusyOperationRunning, Holder: holder}
	}
	return nil
}

// gameProcessRunningLocked reports the game whose process or Steam launch keeps gameID's install running, or whose Steam launch is younger than steamLaunchGrace, clearing older Steam-launch flags once no such process exists; the caller holds s.mu.
func (s *session) gameProcessRunningLocked(gameID string) (string, bool) {
	key := s.fenceKeyLocked(gameID)
	holder := s.runningHolderLocked(gameID, key, s.gamesOnFenceKeyLocked(gameID, key))
	return holder, holder != ""
}

// steamAppIDsLocked returns the Steam app IDs of games, a linked game using its parent's, skipping unknown ones; the caller holds s.mu.
func (s *session) steamAppIDsLocked(games []string) []int {
	var ids []int
	for _, id := range games {
		gc, err := s.config.EffectiveGameConfig(id)
		if err != nil || gc.SteamAppID <= 0 {
			continue
		}
		ids = append(ids, gc.SteamAppID)
	}
	return ids
}

// runningHolderLocked returns the game among games that holds the install root key running, preferring a Steam-launched one, or "" once no process or Steam launch runs from it and every Steam launch flag on it is older than steamLaunchGrace, clearing each such flag unless it was set again meanwhile; the caller holds s.mu.
func (s *session) runningHolderLocked(gameID, key string, games []string) string {
	flagged, launchedAt := s.steamLaunchesAmong(games)
	if !filepath.IsAbs(key) {
		if len(flagged) > 0 {
			return flagged[0]
		}
		return ""
	}
	running, err := s.processRunningIn(key, s.steamAppIDsLocked(games))
	if err != nil {
		slog.Warn("scanning processes for a running game failed; trusting the launch flags", "path", key, "err", err)
		if len(flagged) > 0 {
			return flagged[0]
		}
		return ""
	}
	if running {
		if len(flagged) > 0 {
			return flagged[0]
		}
		return gameID
	}
	now := s.clock()
	for _, id := range flagged {
		if age := now.Sub(launchedAt[id]); age >= 0 && age < steamLaunchGrace {
			return id
		}
	}
	for _, id := range flagged {
		if s.clearSteamLaunchedIfUnchanged(id, launchedAt[id]) {
			slog.Info("cleared a stale Steam launch flag; no process runs from the install", "game", id, "path", key)
		}
	}
	return ""
}

// unmountRunningLocked reports whether a game process, fresh Steam launch, or failed process scan forbids unmounting without clearing any flags; the caller holds s.mu.
func (s *session) unmountRunningLocked(gameID string) bool {
	key := s.fenceKeyLocked(gameID)
	games := s.gamesOnFenceKeyLocked(gameID, key)
	flagged, launchedAt := s.steamLaunchesAmong(games)
	if !filepath.IsAbs(key) {
		return len(flagged) > 0
	}
	running, err := s.processRunningIn(key, s.steamAppIDsLocked(games))
	if err != nil {
		slog.Warn("scanning processes before unmount failed", "game", gameID, "path", key, "err", err)
		return true
	}
	if running {
		return true
	}
	now := s.clock()
	for _, id := range flagged {
		if now.Sub(launchedAt[id]) < steamLaunchGrace {
			return true
		}
	}
	return false
}

// clearSteamLaunchedIfUnchanged clears gameID's Steam-launch flag only while it still carries the launch time at, reporting whether it did.
func (s *session) clearSteamLaunchedIfUnchanged(gameID string, at time.Time) bool {
	s.launchedMu.Lock()
	defer s.launchedMu.Unlock()
	if !s.steamLaunched[gameID] || !s.steamLaunchedAt[gameID].Equal(at) {
		return false
	}
	delete(s.steamLaunched, gameID)
	delete(s.steamLaunchedAt, gameID)
	return true
}

// steamLaunchesAmong returns the games among ids flagged as Steam-launched, in order, with their launch times.
func (s *session) steamLaunchesAmong(ids []string) ([]string, map[string]time.Time) {
	s.launchedMu.Lock()
	defer s.launchedMu.Unlock()
	var flagged []string
	launchedAt := map[string]time.Time{}
	for _, id := range ids {
		if s.steamLaunched[id] {
			flagged = append(flagged, id)
			launchedAt[id] = s.steamLaunchedAt[id]
		}
	}
	return flagged, launchedAt
}

// sharedHeldLocked reports whether a shared reservation for one of ops holds gameID's install root; the caller holds s.mu.
func (s *session) sharedHeldLocked(gameID string, ops ...string) bool {
	key := s.fenceKeyLocked(gameID)
	s.fenceMu.Lock()
	defer s.fenceMu.Unlock()
	for _, holder := range s.fenceShared[key] {
		for _, op := range ops {
			if holder.op == op {
				return true
			}
		}
	}
	return false
}

// releaseExclusive clears the exclusive reservation of holder on key.
func (s *session) releaseExclusive(key string, holder fenceHolder) {
	s.fenceMu.Lock()
	defer s.fenceMu.Unlock()
	if s.fenceExclusive[key] == holder {
		delete(s.fenceExclusive, key)
	}
}

// exclusiveHeld reports whether an exclusive reservation holds key.
func (s *session) exclusiveHeld(key string) bool {
	s.fenceMu.Lock()
	defer s.fenceMu.Unlock()
	_, ok := s.fenceExclusive[key]
	return ok
}

// requireRootDeploymentIdleLocked refuses while game id has an active game-root deployment; the caller holds s.mu for writing.
func (s *session) requireRootDeploymentIdleLocked(gameID, id string) error {
	manager, ok := s.rootDeployMgrs[id]
	if !ok {
		if s.config == nil {
			return nil
		}
		gc, err := s.config.EffectiveGameConfig(id)
		if err != nil || gc.InstallPath == "" {
			return nil
		}
		if manager, err = s.ensureRootDeploymentManager(id, gc); err != nil {
			return fmt.Errorf("checking the game-root deployment of %s: %w", id, err)
		}
	}
	manifest, err := manager.ActiveManifest()
	if err != nil {
		return fmt.Errorf("checking the game-root deployment of %s: %w", id, err)
	}
	if manifest != nil {
		return &dto.OperationBusyError{GameID: gameID, Operation: dto.BusyOperationRootDeployment, Holder: id}
	}
	return nil
}

// firstFenceHolder returns the shared holder that sorts first by operation, then game.
func firstFenceHolder(holders map[uint64]fenceHolder) fenceHolder {
	all := make([]fenceHolder, 0, len(holders))
	for _, holder := range holders {
		all = append(all, holder)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].op != all[j].op {
			return all[i].op < all[j].op
		}
		return all[i].gameID < all[j].gameID
	})
	return all[0]
}
