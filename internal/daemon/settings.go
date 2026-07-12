package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
)

// SetActiveGame records the frontend's currently-displayed game; empty clears the hint.
func (se *SettingsService) SetActiveGame(gameID string) error {
	se.s.activeGameIDMu.Lock()
	se.s.activeGameID = gameID
	se.s.activeGameIDMu.Unlock()
	return nil
}

// GetGameSettings returns the per-game settings (auto_install toggle).
func (se *SettingsService) GetGameSettings(gameID string) (*dto.GameSettingsResult, error) {
	if _, ok := se.s.config.Games[gameID]; !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	s, err := config.LoadGameSettings(gameID)
	if err != nil {
		return nil, err
	}
	return &dto.GameSettingsResult{GameID: gameID, AutoInstall: s.AutoInstall}, nil
}

func (se *SettingsService) SetGameSettings(gameID string, autoInstall bool) (*dto.GameSettingsResult, error) {
	if _, ok := se.s.config.Games[gameID]; !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	s := config.GameSettings{AutoInstall: autoInstall}
	if err := config.SaveGameSettings(gameID, s); err != nil {
		return nil, err
	}
	return &dto.GameSettingsResult{GameID: gameID, AutoInstall: s.AutoInstall}, nil
}

func (se *SettingsService) SetNexusAPIKey(ctx context.Context, apiKey string) (*dto.NexusAPIKeyResult, error) {
	nexus := download.NewNexusClient(apiKey)
	if err := nexus.ValidateAPIKey(ctx); err != nil {
		slog.Warn("nexus API key validation failed", "err", err)
		if errors.Is(err, download.ErrInvalidKey) {
			return &dto.NexusAPIKeyResult{Valid: false, ErrorMessage: "invalid API key"}, nil
		}
		return &dto.NexusAPIKeyResult{Valid: false, ErrorMessage: err.Error()}, nil
	}

	se.s.mu.Lock()
	defer se.s.mu.Unlock()

	se.s.config.NexusAPIKey = apiKey
	if err := se.s.config.Save(); err != nil {
		return nil, fmt.Errorf("saving config: %w", err)
	}

	if se.s.downloadMgr != nil {
		se.s.downloadMgr.Stop()
	}
	se.s.downloadMgr = download.NewManager(nexus, se.s.config, 3, se.s.svc.archives.managerHooks())
	se.s.downloadMgr.SetPostInstallHook(se.s.svc.mods.ensureInModList)
	se.s.downloadMgr.RehydrateLedger()

	slog.Info("nexus API key set and validated")
	return &dto.NexusAPIKeyResult{Valid: true}, nil
}
