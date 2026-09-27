package daemon

import (
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/steam"
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

// steamBaselineLocked reads the install state for the effective game's Steam installation while the caller holds s.mu.
func (s *session) steamBaselineLocked(gameID string, gc config.GameConfig) *vfs.StorefrontSnapshot {
	state, err := s.readSteamAppState(s.mountInstallPath(gc), gc.SteamAppID)
	if err != nil {
		slog.Info("Steam installation state unavailable at activation", "game", gameID, "err", err)
		return nil
	}
	return s.snapshotSteam(state)
}

// snapshotSteam records the Steam manifest values and the time of the comparison.
func (s *session) snapshotSteam(state steam.AppState) *vfs.StorefrontSnapshot {
	return &vfs.StorefrontSnapshot{
		Store: "steam", AppID: state.AppID, BuildID: state.BuildID,
		StateFlags: state.StateFlags, UpdateResult: state.UpdateResult,
		LastUpdated: state.LastUpdated, DepotFingerprint: state.DepotFingerprint,
		CapturedAt: s.clock().UTC(),
	}
}

// steamCaptureLocked decides whether a mounted or crashed farm can be removed and where its output belongs; the caller holds s.mu if the daemon is serving RPCs.
func (s *session) steamCaptureLocked(gameID, dataPath string) (vfs.StorefrontChange, vfs.CaptureOptions, error) {
	var opts vfs.CaptureOptions
	paths := append([]string{dataPath}, vfs.RecoveryFarmCandidates(dataPath)...)
	var sentinel *vfs.Sentinel
	for _, path := range paths {
		found, err := vfs.ReadSentinel(path)
		if errors.Is(err, vfs.ErrSentinelMissing) {
			continue
		}
		if err != nil {
			return vfs.StorefrontBusy, opts, &dto.SteamMaintenanceError{GameID: gameID, Reason: "busy"}
		}
		sentinel = found
		break
	}
	if sentinel == nil {
		return vfs.StorefrontUnknown, opts, nil
	}
	baseline := sentinel.Storefront
	gc, err := s.config.EffectiveGameConfig(gameID)
	if err != nil {
		return vfs.StorefrontBusy, opts, &dto.SteamMaintenanceError{GameID: gameID, Reason: "busy"}
	}
	current, readErr := s.readSteamAppState(s.mountInstallPath(gc), gc.SteamAppID)
	if baseline == nil {
		if readErr == nil && !current.Idle() {
			return vfs.StorefrontBusy, opts, &dto.SteamMaintenanceError{GameID: gameID, Reason: "busy"}
		}
		return vfs.StorefrontUnknown, opts, nil
	}
	change := vfs.CompareStorefront(baseline, current, readErr)
	if change == vfs.StorefrontUnknown || change == vfs.StorefrontBusy {
		return vfs.StorefrontBusy, opts, &dto.SteamMaintenanceError{GameID: gameID, Reason: "busy"}
	}
	if change == vfs.StorefrontChanged {
		opts = vfs.CaptureOptions{PreserveInto: vfs.PreservedDir(dataPath), BatchID: uuid.NewString(), Baseline: baseline, Current: s.snapshotSteam(current)}
	}
	return change, opts, nil
}

// steamAdmissionLocked refuses a farm change while Steam is active or its baseline has changed; the caller holds s.mu.
func (s *session) steamAdmissionLocked(gameID, dataPath string) error {
	if err := s.maintenanceRefusalLocked(gameID, dataPath); err != nil {
		return err
	}
	change, _, err := s.steamCaptureLocked(gameID, dataPath)
	if err != nil {
		return err
	}
	if change == vfs.StorefrontChanged {
		return &dto.SteamMaintenanceError{GameID: gameID, Reason: "verify"}
	}
	if change == vfs.StorefrontUnknown {
		gc, err := s.config.EffectiveGameConfig(gameID)
		if err != nil {
			return err
		}
		state, err := s.readSteamAppState(s.mountInstallPath(gc), gc.SteamAppID)
		if err == nil && !state.Idle() {
			return &dto.SteamMaintenanceError{GameID: gameID, Reason: "busy"}
		}
	}
	return nil
}

// steamAdmission refuses a farm change without requiring the caller to hold s.mu.
func (s *session) steamAdmission(gameID string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	gc, err := s.config.EffectiveGameConfig(gameID)
	if err != nil {
		return err
	}
	subpath := gc.DataSubpath
	if subpath == "" {
		subpath = "Data"
	}
	return s.steamAdmissionLocked(gameID, filepath.Join(s.mountInstallPath(gc), subpath))
}

// steamStatusLocked reports maintenance and retained files for the requested game's deploy folder; the caller holds s.mu.
func (s *session) steamStatusLocked(gameID, dataPath string) (dto.SteamMaintenanceState, []dto.PreservedBatchResult) {
	state := dto.SteamMaintenanceNone
	marker, err := vfs.ReadMaintenance(dataPath)
	if err != nil {
		slog.Warn("reading Steam maintenance marker failed", "game", gameID, "err", err)
		state = dto.SteamMaintenanceBusy
	} else if marker != nil {
		state = dto.SteamMaintenanceVerify
		if marker.Reason == "user" {
			state = dto.SteamMaintenanceUser
		}
	} else {
		change, _, err := s.steamCaptureLocked(gameID, dataPath)
		switch {
		case err != nil, change == vfs.StorefrontBusy:
			state = dto.SteamMaintenanceBusy
		case change == vfs.StorefrontChanged:
			state = dto.SteamMaintenanceVerify
		case change == vfs.StorefrontUnknown:
			gc, configErr := s.config.EffectiveGameConfig(gameID)
			if configErr == nil {
				current, readErr := s.readSteamAppState(s.mountInstallPath(gc), gc.SteamAppID)
				if readErr == nil && !current.Idle() {
					state = dto.SteamMaintenanceBusy
				}
			}
		}
	}
	batches, err := vfs.ListPreservedBatches(dataPath)
	if err != nil {
		slog.Warn("listing preserved Steam files failed", "game", gameID, "err", err)
	}
	results := make([]dto.PreservedBatchResult, 0, len(batches))
	for _, batch := range batches {
		results = append(results, dto.PreservedBatchResult{
			BatchID: batch.BatchID, CreatedAt: batch.CreatedAt.Format(time.RFC3339Nano),
			FileCount: len(batch.Files), Reason: batch.Reason, Path: batch.Path,
		})
	}
	return state, results
}

// maintenanceRefusalLocked refuses mod deployment while a retained verification marker exists; the caller holds s.mu if the daemon is serving RPCs.
func (s *session) maintenanceRefusalLocked(gameID, dataPath string) error {
	marker, err := vfs.ReadMaintenance(dataPath)
	if err != nil {
		slog.Warn("checking Steam maintenance failed", "game", gameID, "err", err)
		return &dto.SteamMaintenanceError{GameID: gameID, Reason: "busy"}
	}
	if marker == nil {
		return nil
	}
	reason := marker.Reason
	if reason != "user" {
		reason = "verify"
	}
	return &dto.SteamMaintenanceError{GameID: gameID, Reason: reason}
}
