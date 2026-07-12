package daemon

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/game"
)

// isSynthetic returns true if the gameID corresponds to a synthetic game definition.
func isSynthetic(gameID string) bool {
	def, ok := game.FindByID(gameID)
	return ok && def.Synthetic
}

func (gs *GameService) ListConfiguredGames() ([]dto.GameInfo, error) {
	gs.s.mu.RLock()
	defer gs.s.mu.RUnlock()

	var games []dto.GameInfo
	for gameID, gc := range gs.s.config.Games {
		subpath := gc.DataSubpath
		if subpath == "" {
			subpath = "Data"
		}
		installPath := gc.InstallPath
		appID := uint32(gc.SteamAppID)
		if gc.LinkedFromGameID != "" {
			if parent, ok := gs.s.config.Games[gc.LinkedFromGameID]; ok {
				installPath = parent.InstallPath
				appID = uint32(parent.SteamAppID)
			}
		}
		vfsActive := false
		if mm, ok := gs.s.mountMgrs[gameID]; ok {
			vfsActive = mm.IsMounted()
		}
		games = append(games, dto.GameInfo{
			GameID:           gameID,
			Name:             gc.Name,
			SteamAppID:       appID,
			InstallPath:      installPath,
			DataPath:         filepath.Join(installPath, subpath),
			Synthetic:        isSynthetic(gameID),
			LinkedFromGameID: gc.LinkedFromGameID,
			VFSActive:        vfsActive,
		})
	}
	return games, nil
}

func (gs *GameService) DetectInstalledGames() ([]dto.GameInfo, error) {
	detected, err := game.DetectInstalledGames()
	if err != nil {
		return nil, err
	}

	detected = gs.applyTTWPlayableProbe(detected)

	gs.s.mu.Lock()
	for _, g := range detected {
		if _, exists := gs.s.config.Games[g.ID]; !exists {
			gc := config.GameConfig{
				Name:             g.Name,
				InstallPath:      g.InstallPath,
				DataSubpath:      g.DataSubpath,
				SteamAppID:       int(g.SteamAppID),
				SteamLibraryPath: g.LibraryPath,
			}
			if g.Synthetic && g.ParentGameID != "" {
				gc.LinkedFromGameID = g.ParentGameID
				gc.SteamAppID = 0
			}
			gs.s.config.Games[g.ID] = gc
			gs.s.ensureMountManager(g.ID, gc)
			slog.Info("auto-configured detected game", "id", g.ID, "path", g.InstallPath, "synthetic", g.Synthetic)
		}
	}
	saveErr := gs.s.config.Save()
	gs.s.mu.Unlock()
	if saveErr != nil {
		return nil, saveErr
	}
	if err := gs.s.svc.execs.syncInstalledManagedLOOT(); err != nil {
		slog.Warn("could not register installed LOOT for detected games", "err", err)
	}

	var games []dto.GameInfo
	for _, g := range detected {
		vfsActive := false
		if mm, ok := gs.s.mountMgrs[g.ID]; ok {
			vfsActive = mm.IsMounted()
		}
		games = append(games, dto.GameInfo{
			GameID:           g.ID,
			Name:             g.Name,
			SteamAppID:       g.SteamAppID,
			InstallPath:      g.InstallPath,
			DataPath:         g.DataPath,
			Synthetic:        g.Synthetic,
			LinkedFromGameID: g.ParentGameID,
			VFSActive:        vfsActive,
		})
	}
	return games, nil
}

// applyTTWPlayableProbe is the daemon-side TTWPlayableProbe wired into game.AppendSyntheticGames.
func (gs *GameService) applyTTWPlayableProbe(detected []game.DetectedGame) []game.DetectedGame {
	probe := func() (string, bool) {
		var fnvInstall string
		for _, g := range detected {
			if g.ID == "falloutnv" {
				fnvInstall = g.InstallPath
				break
			}
		}
		if fnvInstall == "" {
			return "", false
		}
		if !game.HasTTWMarker(fnvInstall) {
			return "", false
		}
		entries, err := os.ReadDir(config.ModsDir("ttw"))
		if err != nil {
			return "", false
		}
		for _, e := range entries {
			if e.IsDir() && e.Name() != "Downloads" && e.Name() != "Overwrite" {
				return fnvInstall, true
			}
		}
		return "", false
	}
	return game.AppendSyntheticGames(detected, probe)
}

// ConfigureGame persists a game to the daemon's config and creates its mount manager.
func (gs *GameService) ConfigureGame(gameID, name string, steamAppID uint32, installPath, dataSubpath string) error {
	gs.s.mu.Lock()

	if dataSubpath == "" {
		dataSubpath = "Data"
	}

	gc := config.GameConfig{
		Name:        name,
		InstallPath: installPath,
		DataSubpath: dataSubpath,
		SteamAppID:  int(steamAppID),
	}
	gs.s.config.Games[gameID] = gc
	gs.s.ensureMountManager(gameID, gc)

	if err := gs.s.config.Save(); err != nil {
		gs.s.mu.Unlock()
		return fmt.Errorf("saving config after configuring game %s: %w", gameID, err)
	}
	gs.s.mu.Unlock()
	if err := gs.s.svc.execs.syncInstalledManagedLOOT(); err != nil {
		slog.Warn("could not register installed LOOT after configuring game", "game", gameID, "err", err)
	}

	slog.Info("game configured", "id", gameID, "path", installPath)
	return nil
}
