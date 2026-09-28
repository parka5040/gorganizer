package daemon

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/game"
	"github.com/parka/gorganizer/internal/gamedef"
	"github.com/parka/gorganizer/internal/tools"
)

// isSynthetic returns true if the gameID corresponds to a synthetic game definition.
func isSynthetic(gameID string) bool {
	def, ok := game.FindByID(gameID)
	return ok && def.Synthetic
}

// capabilitiesFor derives the GUI feature set for a game from its registry definition, or nil when unknown.
func capabilitiesFor(gameID string) *dto.GameCapabilities {
	def, ok := gamedef.ByID(gameID)
	if !ok {
		return nil
	}
	_, loot := tools.LOOTGameID(gameID)
	caps := &dto.GameCapabilities{
		Plugins:              def.Plugins != nil,
		Ini:                  def.Ini != nil,
		Loot:                 loot,
		ModLoader:            dto.ModLoaderKindNone,
		InstallLayout:        installLayoutResult(def.Layout),
		ManifestDependencies: def.Layout == gamedef.LayoutSMAPIManifest,
	}
	if def.ModLoader != nil {
		caps.ModLoader = modLoaderKindResult(def.ModLoader.Kind)
	}
	return caps
}

// modLoaderKindResult maps a registry mod-loader kind to its wire value.
func modLoaderKindResult(kind gamedef.ModLoaderKind) dto.ModLoaderKindResult {
	switch kind {
	case gamedef.ModLoaderSMAPI:
		return dto.ModLoaderKindSMAPI
	default:
		return dto.ModLoaderKindNone
	}
}

// installLayoutResult maps a registry install layout to its wire value, unknown layouts to Unspecified.
func installLayoutResult(layout gamedef.InstallLayout) dto.InstallLayoutResult {
	switch layout {
	case gamedef.LayoutDataRoot:
		return dto.InstallLayoutDataRoot
	case gamedef.LayoutSMAPIManifest:
		return dto.InstallLayoutSMAPIManifest
	default:
		return dto.InstallLayoutUnspecified
	}
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
			Capabilities:     capabilitiesFor(gameID),
		})
	}
	return games, nil
}

// DetectInstalledGames configures every newly detected game, recovers interrupted mod-loader transactions at their installs once startup recovery finished, and lists the detected games.
func (gs *GameService) DetectInstalledGames() ([]dto.GameInfo, error) {
	gs.s.mu.RLock()
	configured := make(map[string]string, len(gs.s.config.Games))
	for id, gc := range gs.s.config.Games {
		configured[id] = gc.InstallPath
	}
	gs.s.mu.RUnlock()
	detected, err := game.DetectInstalledGamesWithPaths(configured)
	if err != nil {
		return nil, err
	}

	retained := detected[:0]
	for _, g := range detected {
		if path := configured[g.ID]; path != "" {
			configuredPath, configuredErr := filepath.EvalSymlinks(path)
			detectedPath, detectedErr := filepath.EvalSymlinks(g.InstallPath)
			if configuredErr != nil || detectedErr != nil || configuredPath != detectedPath {
				slog.Warn("detected Steam install differs from configured game; keeping configured install", "game", g.ID, "configured", path, "detected", g.InstallPath)
				continue
			}
			g.InstallPath = path
			g.DataPath = filepath.Join(path, g.DataSubpath)
		}
		retained = append(retained, g)
	}
	detected = gs.applyTTWPlayableProbe(retained)

	gs.s.mu.Lock()
	var added []string
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
			added = append(added, g.ID)
			slog.Info("auto-configured detected game", "id", g.ID, "path", g.InstallPath, "synthetic", g.Synthetic)
		}
	}
	saveErr := gs.s.config.Save()
	gs.s.mu.Unlock()
	if len(added) > 0 {
		if err := gs.s.awaitRecovery(); err != nil {
			slog.Warn("mod-loader recovery of detected games skipped; mounting and launching stay refused while an intent exists", "games", added, "err", err)
		} else {
			gs.s.recoverAddedLoaderGames(added)
		}
	}
	if saveErr != nil {
		return nil, saveErr
	}
	if err := gs.s.svc.execs.syncInstalledManagedLOOT(); err != nil {
		slog.Warn("could not register installed LOOT for detected games", "err", err)
	}

	var games []dto.GameInfo
	for _, g := range detected {
		vfsActive := false
		gs.s.mu.RLock()
		if mm, ok := gs.s.mountMgrs[g.ID]; ok {
			vfsActive = mm.IsMounted()
		}
		gs.s.mu.RUnlock()
		games = append(games, dto.GameInfo{
			GameID:           g.ID,
			Name:             g.Name,
			SteamAppID:       g.SteamAppID,
			InstallPath:      g.InstallPath,
			DataPath:         g.DataPath,
			Synthetic:        g.Synthetic,
			LinkedFromGameID: g.ParentGameID,
			VFSActive:        vfsActive,
			Capabilities:     capabilitiesFor(g.ID),
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

// defaultDataSubpath returns the registry deploy subpath for a known game, or "Data" for an unknown one.
func defaultDataSubpath(gameID string) string {
	if def, ok := gamedef.ByID(gameID); ok && def.DataSubpath != "" {
		return def.DataSubpath
	}
	return "Data"
}

// ConfigureGame waits for startup recovery, then persists a game to the daemon's config, creates its mount manager, and recovers an interrupted mod-loader transaction at its install.
func (gs *GameService) ConfigureGame(gameID, name string, steamAppID uint32, installPath, dataSubpath string) error {
	if err := gs.s.awaitRecovery(); err != nil {
		return err
	}
	gs.s.mu.Lock()
	release, err := gs.s.reserveShared(gameID, dto.BusyOperationConfigure)
	if err != nil {
		gs.s.mu.Unlock()
		return err
	}
	defer release()

	if dataSubpath == "" {
		dataSubpath = defaultDataSubpath(gameID)
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
	release()
	gs.s.recoverAddedLoaderGames([]string{gameID})
	if err := gs.s.svc.execs.syncInstalledManagedLOOT(); err != nil {
		slog.Warn("could not register installed LOOT after configuring game", "game", gameID, "err", err)
	}

	slog.Info("game configured", "id", gameID, "path", installPath)
	return nil
}
