package daemon

import (
	"context"
	"errors"
	"fmt"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/tools"
)

func (es *ExecutableService) syncInstalledManagedLOOT() error {
	status, err := es.s.lootInstaller.Status()
	if err != nil || !status.Installed {
		return err
	}
	return es.syncManagedLOOT(status)
}

func (es *ExecutableService) sweepLOOTWorkspaces() error {
	es.s.mu.RLock()
	configured := make(map[string]config.GameConfig, len(es.s.config.Games))
	for gameID, gameConfig := range es.s.config.Games {
		configured[gameID] = gameConfig
	}
	es.s.mu.RUnlock()

	seen := make(map[string]bool)
	var sweepErrors []error
	for gameID, gameConfig := range configured {
		if gameConfig.LinkedFromGameID != "" {
			parent, ok := configured[gameConfig.LinkedFromGameID]
			if !ok {
				continue
			}
			gameConfig.InstallPath = parent.InstallPath
			gameConfig.SteamAppID = parent.SteamAppID
			gameConfig.SteamLibraryPath = parent.SteamLibraryPath
		}
		if gameConfig.SteamAppID <= 0 {
			continue
		}
		library, err := tools.ResolveSteamLibrary(&gameConfig)
		if err != nil {
			sweepErrors = append(sweepErrors, fmt.Errorf("resolving %s Steam library: %w", gameID, err))
			continue
		}
		key := fmt.Sprintf("%s\x00%d", library, gameConfig.SteamAppID)
		if seen[key] {
			continue
		}
		seen[key] = true
		if err := tools.SweepLOOTWorkspaces(library, gameConfig.SteamAppID); err != nil {
			sweepErrors = append(sweepErrors, fmt.Errorf("sweeping %s LOOT workspaces: %w", gameID, err))
		}
	}
	return errors.Join(sweepErrors...)
}

func managedStatusToDTO(status tools.ManagedToolStatus) dto.ManagedToolStatusResult {
	return dto.ManagedToolStatusResult{
		ID: status.ID, Installed: status.Installed, ActiveVersion: status.ActiveVersion,
		PreviousVersion: status.PreviousVersion, ExecutablePath: status.ExecutablePath,
		UpdateAvailable: status.UpdateAvailable,
	}
}

// GetManagedToolStatus returns local managed-tool state without silently checking for updates.
func (es *ExecutableService) GetManagedToolStatus(toolID string) (dto.ManagedToolStatusResult, error) {
	if toolID != "loot" {
		return dto.ManagedToolStatusResult{}, fmt.Errorf("unsupported managed tool %q", toolID)
	}
	status, err := es.s.lootInstaller.Status()
	if err != nil {
		return dto.ManagedToolStatusResult{}, err
	}
	return managedStatusToDTO(status), nil
}

// InstallManagedTool explicitly downloads and activates the latest stable managed-tool release.
func (es *ExecutableService) InstallManagedTool(ctx context.Context, toolID string) (dto.ManagedToolStatusResult, error) {
	if toolID != "loot" {
		return dto.ManagedToolStatusResult{}, fmt.Errorf("unsupported managed tool %q", toolID)
	}
	status, err := es.s.lootInstaller.InstallLatest(ctx)
	if err != nil {
		return dto.ManagedToolStatusResult{}, err
	}
	if err := es.syncManagedLOOT(status); err != nil {
		return dto.ManagedToolStatusResult{}, err
	}
	return managedStatusToDTO(status), nil
}

// RollbackManagedTool atomically reactivates the retained previous release.
func (es *ExecutableService) RollbackManagedTool(toolID string) (dto.ManagedToolStatusResult, error) {
	if toolID != "loot" {
		return dto.ManagedToolStatusResult{}, fmt.Errorf("unsupported managed tool %q", toolID)
	}
	status, err := es.s.lootInstaller.Rollback()
	if err != nil {
		return dto.ManagedToolStatusResult{}, err
	}
	if err := es.syncManagedLOOT(status); err != nil {
		return dto.ManagedToolStatusResult{}, err
	}
	return managedStatusToDTO(status), nil
}

func (es *ExecutableService) syncManagedLOOT(status tools.ManagedToolStatus) error {
	if !status.Installed || status.ExecutablePath == "" {
		return nil
	}
	es.s.mu.Lock()
	defer es.s.mu.Unlock()
	for gameID, game := range es.s.config.Games {
		if _, ok := tools.LOOTGameID(gameID); !ok {
			continue
		}
		executable := config.Executable{
			ID: "managed-loot", Title: "LOOT", ExePath: status.ExecutablePath, ToolID: "loot",
			Runner: string(tools.RunnerProton), NeedsVFSMounted: true,
			OutputPolicy: string(tools.OutputProfileSync), SanitizeEnv: true, AutoDetected: true,
		}
		replaced := false
		for index := range game.Executables {
			if game.Executables[index].ID == executable.ID || game.Executables[index].ToolID == "loot" {
				game.Executables[index] = executable
				replaced = true
				break
			}
		}
		if !replaced {
			game.Executables = append(game.Executables, executable)
		}
		es.s.config.Games[gameID] = game
	}
	return es.s.config.Save()
}
