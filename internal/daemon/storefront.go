package daemon

import (
	"fmt"
	"log/slog"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/vfs"
)

// steamStateFor compares a live Steam installation with its mounted farm's baseline.
func (s *session) steamStateFor(gameID string) (vfs.StorefrontChange, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.steamStateForLocked(gameID)
}

// steamStateForLocked compares the mounted farm's baseline with Steam while the caller holds s.mu.
func (s *session) steamStateForLocked(gameID string) (vfs.StorefrontChange, error) {
	gc, err := s.config.EffectiveGameConfig(gameID)
	if err != nil {
		return vfs.StorefrontUnknown, err
	}
	mm, ok := s.mountMgrs[gameID]
	if !ok || !mm.IsMounted() {
		return vfs.StorefrontUnknown, nil
	}
	baseline, err := mm.StorefrontBaseline()
	if err != nil {
		return vfs.StorefrontUnknown, fmt.Errorf("reading storefront baseline for %s: %w", gameID, err)
	}
	if baseline == nil {
		return vfs.StorefrontUnknown, nil
	}
	current, readErr := s.readSteamAppState(s.mountInstallPath(gc), gc.SteamAppID)
	return vfs.CompareStorefront(baseline, current, readErr), readErr
}

// logSteamStateLocked logs the current storefront comparison while the caller holds s.mu.
func (s *session) logSteamStateLocked(gameID, operation string) {
	change, err := s.steamStateForLocked(gameID)
	slog.Debug("Steam installation state before farm operation", "game", gameID, "operation", operation, "change", change, "err", err)
}

// logSteamState logs the current storefront comparison before a launch.
func (s *session) logSteamState(gameID, operation string) {
	change, err := s.steamStateFor(gameID)
	slog.Debug("Steam installation state before farm operation", "game", gameID, "operation", operation, "change", change, "err", err)
}

// steamBaselineLocked reads the install state for the effective game's Steam installation while the caller holds s.mu.
func (s *session) steamBaselineLocked(gameID string, gc config.GameConfig) *vfs.StorefrontSnapshot {
	state, err := s.readSteamAppState(s.mountInstallPath(gc), gc.SteamAppID)
	if err != nil {
		slog.Info("Steam installation state unavailable at activation", "game", gameID, "err", err)
		return nil
	}
	return &vfs.StorefrontSnapshot{
		Store: "steam", AppID: state.AppID, BuildID: state.BuildID,
		StateFlags: state.StateFlags, UpdateResult: state.UpdateResult,
		LastUpdated: state.LastUpdated, DepotFingerprint: state.DepotFingerprint,
		CapturedAt: s.now().UTC(),
	}
}
