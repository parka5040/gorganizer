package daemon

import (
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/gamedef"
	inipkg "github.com/parka/gorganizer/internal/ini"
	"github.com/parka/gorganizer/internal/plugins"
	"github.com/parka/gorganizer/internal/tools"
)

func (ls *LaunchService) LaunchGame(gameID string, useTool bool, profileName string) (int, error) {
	if err := ls.s.awaitRecovery(); err != nil {
		return 0, err
	}
	if pending := ls.s.recoveryPendingFor(gameID); pending != nil {
		return 0, fmt.Errorf("recovery pending for %s: %s — confirm via the GUI prompt or `gorganizerctl recover-confirm` first",
			gameID, pending.Reason)
	}
	if conflict := ls.s.findMutexConflict(gameID); conflict != "" {
		return 0, &VFSMutexError{
			GameID:      gameID,
			Conflicting: conflict,
			Group:       mutexGroupOf(gameID),
		}
	}
	gc, ok := ls.s.config.Games[gameID]
	if !ok {
		return 0, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	if gc.LinkedFromGameID != "" {
		if _, parentOk := ls.s.config.Games[gc.LinkedFromGameID]; !parentOk {
			return 0, &ErrLinkedParentMissing{
				GameID:       gameID,
				ParentGameID: gc.LinkedFromGameID,
			}
		}
	}
	if gc.LinkedFromGameID != "" {
		eff, err := ls.s.config.EffectiveGameConfig(gameID)
		if err != nil {
			return 0, err
		}
		gc = eff
	}

	if isSynthetic(gameID) {
		if err := ls.s.svc.ttw.VerifyTTWIntegrity(); err != nil {
			return 0, err
		}
	}

	mm := ls.s.ensureMountManager(gameID, gc)
	if !mm.IsMounted() && profileName != "" {
		slog.Info("auto-mounting VFS before launch", "game", gameID, "profile", profileName)
		if _, err := ls.s.svc.vfs.MountVFS(gameID, profileName); err != nil {
			return 0, fmt.Errorf("auto-mount of %s VFS failed: %w", gameID, err)
		}
	}

	if mm.IsMounted() && mm.IsDirty() && !ls.s.mountBusy(gameID) {
		slog.Info("applying pending mod changes before launch", "game", gameID)
		if err := ls.s.svc.vfs.RebuildVFS(gameID); err != nil {
			return 0, fmt.Errorf("applying pending mod changes before launch: %w", err)
		}
	}
	if mm.IsMounted() {
		if err := ls.s.applyRootDeployment(gameID, gc, profileName); err != nil {
			return 0, fmt.Errorf("applying game-root deployment before launch: %w", err)
		}
	}

	if profileName == "" {
		slog.Warn("no profile selected — skipping INI push (tweaks like disable-intro will NOT apply)",
			"game", gameID)
	} else {
		p, _, err := ls.s.profileMgr.Load(gameID, profileName)
		switch {
		case err != nil:
			slog.Warn("profile load failed — skipping INI push", "game", gameID, "profile", profileName, "err", err)
		case !p.UseCustomIni:
			slog.Warn("profile has UseCustomIni=false — skipping INI push (tweaks will NOT apply; enable 'Use custom INIs' in profile settings)",
				"game", gameID, "profile", profileName)
		default:
			if _, ok := inipkg.SpecFor(gameID); !ok {
				slog.Info("no INI spec for game — skipping INI push", "game", gameID)
			} else {
				compatData, _ := tools.ResolveCompatDataPath(&gc, 0)
				reports, err := ls.s.iniMgr.PushToDocumentsAt(gameID, profileName, gc.SteamAppID, compatData)
				if err != nil {
					if useTool {
						return 0, fmt.Errorf("pushing profile INIs failed: %w", err)
					}
					slog.Warn("pushing profile INIs failed", "game", gameID, "profile", profileName, "err", err)
				}
				var unverified []string
				for _, r := range reports {
					if r.Skipped {
						slog.Info("INI push skipped",
							"name", r.Filename, "target", r.TargetPath, "reason", r.Note)
						continue
					}
					slog.Info("INI pushed",
						"name", r.Filename, "target", r.TargetPath,
						"bytes", r.Bytes, "sha256", r.SHA256,
						"mtime", r.ModTime, "verified", r.Verified, "note", r.Note)
					if !r.Verified {
						unverified = append(unverified, r.Filename+": "+r.Note)
					}
				}
				if len(unverified) > 0 && useTool {
					return 0, fmt.Errorf("INI push verification failed for: %s", strings.Join(unverified, "; "))
				}
			}
		}
	}

	if profileName != "" {
		if err := ls.writePluginsTxt(gameID, gc, profileName); err != nil {
			slog.Warn("writing plugins.txt failed", "game", gameID, "err", err)
		}
	}

	if useTool {
		if ls.s.toolMgr == nil {
			return 0, fmt.Errorf("tool launch requested but tool manager is not initialized")
		}
		if err := tools.ValidateSKSERuntime(gameID, gc.InstallPath); err != nil {
			return 0, err
		}
		if drifted, verr := VerifyScriptExtenderManifest(gc.InstallPath); verr == nil && len(drifted) > 0 {
			slog.Warn("script extender manifest drift — refusing to launch",
				"game", gameID, "drifted_files", drifted)
			return 0, &tools.LoaderMissingError{
				GameID:        gameID,
				ConfiguredExe: gc.ToolExe,
				InstallPath:   gc.InstallPath,
				Reason:        "modified",
			}
		} else if verr != nil {
			slog.Warn("could not verify script extender manifest", "err", verr)
		}
		handle, err := ls.s.toolMgr.LaunchGame(gameID, true, &gc, ls.s.config.PreferredProton)
		if err != nil {
			return 0, fmt.Errorf("launching via script extender: %w", err)
		}
		ls.s.trackLaunched(gameID, handle)
		return handle.PID, nil
	}

	steamURL := fmt.Sprintf("steam://rungameid/%d", gc.SteamAppID)
	cmd := exec.Command("xdg-open", steamURL)
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("launching via Steam: %w", err)
	}
	go cmd.Wait()
	ls.s.setSteamLaunched(gameID, true)
	return cmd.Process.Pid, nil
}

// writePluginsTxt materializes the engine-readable plugins.txt into AppData/Local/{GameSubdir}/.
func (ls *LaunchService) writePluginsTxt(gameID string, gc config.GameConfig, profileName string, destinationOverride ...string) error {
	spec, ok := plugins.SpecFor(gameID)
	if !ok {
		return nil
	}

	_, entries, err := ls.s.profileMgr.Load(gameID, profileName)
	if err != nil {
		return fmt.Errorf("loading profile %q: %w", profileName, err)
	}

	modsDir := config.ModsDir(gameID)
	var enabled []plugins.ModEntry
	for _, e := range entries {
		if !e.Enabled {
			continue
		}
		enabled = append(enabled, plugins.ModEntry{
			Name: e.Name,
			Path: filepath.Join(modsDir, e.Name),
		})
	}

	subpath := gc.DataSubpath
	if subpath == "" {
		subpath = "Data"
	}
	baseData := filepath.Join(gc.InstallPath, subpath)
	discoveryMods := enabled
	ls.s.mu.RLock()
	mm, mounted := ls.s.mountMgrs[gameID]
	ls.s.mu.RUnlock()
	if mounted && mm.IsMounted() {
		discoveryMods = nil
	}

	list, err := plugins.DiscoverPlugins(baseData, discoveryMods, spec)
	if err != nil {
		return fmt.Errorf("discovering plugins: %w", err)
	}
	plugins.ApplyCanonicalOrder(list, spec)
	seedDir := baseData
	if mounted && mm.IsMounted() {
		seedDir = mm.BackupPath()
	}
	if err := applyProfilePluginLoadout(ls.s.profileMgr, gameID, profileName, seedDir, spec, list); err != nil {
		return fmt.Errorf("loading plugin loadout: %w", err)
	}

	destDir := baseData
	if len(destinationOverride) > 0 && destinationOverride[0] != "" {
		destDir = destinationOverride[0]
	} else if spec.StateLocation == gamedef.PluginStateGameRootIni {
		destDir = gc.InstallPath
	} else if spec.StateLocation != gamedef.PluginStateDataDir {
		compatData, resolveErr := tools.ResolveCompatDataPath(&gc, 0)
		if resolveErr == nil {
			destDir, err = inipkg.AppDataLocalPathAt(compatData, spec.AppDataSubdir)
		} else {
			destDir, err = inipkg.AppDataLocalPath(gc.SteamAppID, spec.AppDataSubdir)
		}
		if err != nil {
			return fmt.Errorf("resolving AppData path: %w", err)
		}
	}

	if err := plugins.Write(spec, destDir, list); err != nil {
		return fmt.Errorf("writing plugins.txt: %w", err)
	}
	slog.Info("plugins.txt deployed",
		"game", gameID, "profile", profileName,
		"count", len(list), "dest", destDir)
	return nil
}

// GetPreferredProton returns the global Proton preference or "" for auto-pick.
func (ls *LaunchService) GetPreferredProton() (string, error) {
	return ls.s.config.PreferredProton, nil
}

// SetPreferredProton stores a global Proton path override; empty clears it.
func (ls *LaunchService) SetPreferredProton(path string) error {
	ls.s.config.PreferredProton = path
	return ls.s.config.Save()
}

func (ls *LaunchService) DetectProton() ([]dto.ProtonVersionResult, error) {
	if ls.s.toolMgr == nil {
		return nil, nil
	}
	return ls.s.toolMgr.DetectProton()
}
