package daemon

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/smapi"
	"github.com/parka/gorganizer/internal/steam"
	"github.com/parka/gorganizer/internal/vfs"
)

type OfflineRecoveryStep struct {
	Recovered bool
	Pending   string
}

type OfflineRecoveryReport struct {
	Loader OfflineRecoveryStep
	Root   OfflineRecoveryStep
	Data   OfflineRecoveryStep
}

// RecoverGameOffline checks the loader transaction, game-root deployment and Data farm for a configured game without starting a daemon.
func RecoverGameOffline(cfg *config.Config, gameID string) (OfflineRecoveryReport, error) {
	var report OfflineRecoveryReport
	if cfg == nil {
		return report, fmt.Errorf("recovering %s: missing game configuration", gameID)
	}
	gc, err := cfg.EffectiveGameConfig(gameID)
	if err != nil {
		return report, fmt.Errorf("resolving %s: %w", gameID, err)
	}
	if gc.InstallPath == "" {
		return report, fmt.Errorf("recovering %s: no install path is configured", gameID)
	}
	dataSubpath := gc.DataSubpath
	if dataSubpath == "" {
		dataSubpath = "Data"
	}
	s := &session{config: cfg, rootDeployMgrs: make(map[string]*vfs.RootDeploymentManager), readSteamAppState: steam.ReadAppState}
	dataPath := filepath.Join(s.mountInstallPath(gc), dataSubpath)
	var failures []error
	loaderDeferred := false
	if _, _, ok := loaderSpecFor(gameID); ok && loaderStatePresent(gc.InstallPath) {
		report.Loader, err = recoverOfflineLoader(gc.InstallPath)
		if errors.Is(err, smapi.ErrTransactionActive) {
			loaderDeferred = true
		} else if err != nil {
			report.Loader.Pending = err.Error()
			failures = append(failures, fmt.Errorf("recovering SMAPI: %w", err))
		}
	}

	manager, rootErr := s.ensureRootDeploymentManager(gameID, gc)
	if rootErr == nil {
		var outcome vfs.RootRecoveryOutcome
		outcome, rootErr = manager.Recover()
		report.Root.Recovered = outcome.Recovered
		if outcome.Pending != nil {
			report.Root.Pending = outcome.Pending.Reason
		}
	}
	if rootErr != nil {
		report.Root.Pending = rootErr.Error()
		failures = append(failures, fmt.Errorf("recovering game-root files: %w", rootErr))
	}

	_, capture, steamErr := s.steamCaptureLocked(gameID, dataPath)
	var outcome vfs.RecoveryOutcome
	var dataErr error
	if steamErr != nil {
		report.Data.Pending = steamErr.Error()
	} else {
		outcome, dataErr = vfs.CleanupStale(dataPath, capture)
	}
	if dataErr != nil {
		report.Data.Pending = dataErr.Error()
		failures = append(failures, fmt.Errorf("recovering Data folder: %w", dataErr))
	} else if steamErr == nil {
		report.Data.Recovered = outcome.Restored || outcome.FuseUnmounted
		if outcome.Pending != nil {
			report.Data.Pending = outcome.Pending.Reason
		}
	}

	if loaderDeferred {
		report.Loader, err = recoverOfflineLoader(gc.InstallPath)
		if err != nil {
			report.Loader.Pending = err.Error()
			if !errors.Is(err, smapi.ErrTransactionActive) {
				failures = append(failures, fmt.Errorf("recovering SMAPI: %w", err))
			}
		}
	}
	if manager != nil && rootErr == nil && report.Root.Pending == "" && report.Data.Pending == "" && report.Loader.Pending == "" {
		manifest, err := manager.ActiveManifest()
		if err != nil {
			report.Root.Pending = err.Error()
			failures = append(failures, fmt.Errorf("checking game-root files: %w", err))
		} else if manifest != nil {
			if _, err := manager.Deactivate(); err != nil {
				report.Root.Pending = err.Error()
				failures = append(failures, fmt.Errorf("restoring game-root files: %w", err))
			} else {
				report.Root.Recovered = true
			}
		}
	}
	return report, errors.Join(failures...)
}

// recoverOfflineLoader resolves a pending SMAPI loader transaction and reports whether it changed anything.
func recoverOfflineLoader(gameDir string) (OfflineRecoveryStep, error) {
	result, err := (smapiEngine{}).Recover(gameDir)
	if err != nil {
		return OfflineRecoveryStep{}, err
	}
	return OfflineRecoveryStep{
		Recovered: result.RolledBack || result.Committed || result.SweptWorkDirs > 0 || result.SweptBackups > 0,
	}, nil
}
