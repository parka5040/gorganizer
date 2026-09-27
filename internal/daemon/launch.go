package daemon

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/gamedef"
	inipkg "github.com/parka/gorganizer/internal/ini"
	"github.com/parka/gorganizer/internal/plugins"
	"github.com/parka/gorganizer/internal/profile"
	"github.com/parka/gorganizer/internal/smapi"
	"github.com/parka/gorganizer/internal/tools"
	"github.com/parka/gorganizer/internal/vfs"
)

// LaunchGame mounts and applies gameID's farm as needed and starts the game through Steam or its script extender, refusing while pending mod changes cannot be applied because the game still runs.
func (ls *LaunchService) LaunchGame(gameID string, useTool bool, profileName string) (int, error) {
	if err := ls.s.awaitRecovery(); err != nil {
		return 0, err
	}
	gc, mm, reservation, err := ls.admitLaunch(gameID)
	if err != nil {
		return 0, err
	}
	defer reservation.Release()
	if err := ls.s.launchStep("admitted"); err != nil {
		return 0, err
	}

	if isSynthetic(gameID) {
		if err := ls.s.svc.ttw.VerifyTTWIntegrity(); err != nil {
			return 0, err
		}
	}

	if err := ls.loaderPreflight(gameID, mm, profileName); err != nil {
		return 0, err
	}
	if !mm.IsMounted() && profileName != "" {
		slog.Info("auto-mounting VFS before launch", "game", gameID, "profile", profileName)
		if _, err := ls.s.svc.vfs.mountVFSOwned(gameID, profileName, false, reservation.id); err != nil {
			return 0, fmt.Errorf("auto-mount of %s VFS failed: %w", gameID, err)
		}
	}

	if mm.IsMounted() && mm.IsDirty() {
		if err := ls.refuseDirtyRunningFarm(gameID); err != nil {
			return 0, err
		}
		slog.Info("applying pending mod changes before launch", "game", gameID)
		if err := ls.s.svc.vfs.rebuildVFSOwned(gameID, reservation.id); err != nil {
			return 0, fmt.Errorf("applying pending mod changes before launch: %w", err)
		}
	}
	if err := ls.s.applyMountedRootDeployment(gameID, gc, profileName, mm); err != nil {
		return 0, fmt.Errorf("applying game-root deployment before launch: %w", err)
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
		preferred := ls.s.preferredProton()
		handle, err := ls.s.toolMgr.LaunchGame(gameID, true, &gc, preferred)
		if err != nil {
			return 0, fmt.Errorf("launching via script extender: %w", err)
		}
		ls.s.trackLaunched(gameID, handle)
		return handle.PID, nil
	}

	pid, err := ls.s.openSteamURL(fmt.Sprintf("steam://rungameid/%d", gc.SteamAppID))
	if err != nil {
		return 0, fmt.Errorf("launching via Steam: %w", err)
	}
	ls.s.setSteamLaunched(gameID, true)
	return pid, nil
}

var defaultSteamOpener = xdgOpenURL

// openSteamURL hands a steam:// URL to the session's opener, or to the package default every session inherits, and returns the opener's PID.
func (s *session) openSteamURL(url string) (int, error) {
	if s.steamOpener != nil {
		return s.steamOpener(url)
	}
	return defaultSteamOpener(url)
}

// xdgOpenURL hands url to the desktop's URL opener and returns its PID.
func xdgOpenURL(url string) (int, error) {
	cmd := exec.Command("xdg-open", url)
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	go cmd.Wait()
	return cmd.Process.Pid, nil
}

// admitLaunch refuses, under s.mu, a launch of gameID while a recovery is pending or a mutex sibling is mounted, then takes its shared launch reservation and resolves its effective config and mount manager.
func (ls *LaunchService) admitLaunch(gameID string) (config.GameConfig, *vfs.MountManager, sharedReservation, error) {
	ls.s.mu.Lock()
	defer ls.s.mu.Unlock()
	if pending := ls.s.recoveryPendingFor(gameID); pending != nil {
		return config.GameConfig{}, nil, sharedReservation{}, fmt.Errorf("recovery pending for %s: %s — confirm via the GUI prompt or `gorganizerctl recover-confirm` first",
			gameID, pending.Reason)
	}
	if conflict := ls.s.findMutexConflict(gameID); conflict != "" {
		return config.GameConfig{}, nil, sharedReservation{}, &VFSMutexError{
			GameID:      gameID,
			Conflicting: conflict,
			Group:       mutexGroupOf(gameID),
		}
	}
	reservation, err := ls.s.reserveSharedOwned(gameID, dto.BusyOperationLaunch)
	if err != nil {
		return config.GameConfig{}, nil, sharedReservation{}, err
	}
	gc, ok := ls.s.config.Games[gameID]
	if !ok {
		reservation.Release()
		return config.GameConfig{}, nil, sharedReservation{}, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	if gc.LinkedFromGameID != "" {
		if _, parentOk := ls.s.config.Games[gc.LinkedFromGameID]; !parentOk {
			reservation.Release()
			return config.GameConfig{}, nil, sharedReservation{}, &ErrLinkedParentMissing{
				GameID:       gameID,
				ParentGameID: gc.LinkedFromGameID,
			}
		}
		eff, err := ls.s.config.EffectiveGameConfig(gameID)
		if err != nil {
			reservation.Release()
			return config.GameConfig{}, nil, sharedReservation{}, err
		}
		gc = eff
	}
	return cloneGameConfig(gc), ls.s.ensureMountManager(gameID, gc), reservation, nil
}

// refuseDirtyRunningFarm returns a GameRunningError while a tracked launch or tool, a game process, or a fresh Steam launch may still read gameID's farm, so pending changes are never skipped silently before a launch.
func (ls *LaunchService) refuseDirtyRunningFarm(gameID string) error {
	if !ls.s.applyBusy(gameID) {
		return nil
	}
	return &dto.GameRunningError{GameID: gameID, Operation: dto.GameRunningOperationLaunch}
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
	return ls.s.preferredProton(), nil
}

// SetPreferredProton stores a global Proton path override; empty clears it.
func (ls *LaunchService) SetPreferredProton(path string) error {
	ls.s.mu.Lock()
	defer ls.s.mu.Unlock()
	ls.s.config.PreferredProton = path
	return ls.s.config.Save()
}

func (ls *LaunchService) DetectProton() ([]dto.ProtonVersionResult, error) {
	if ls.s.toolMgr == nil {
		return nil, nil
	}
	return ls.s.toolMgr.DetectProton()
}

// loaderPreflight refuses any launch while a mod-loader intent is unresolved, and a launch whose effective mod set holds SMAPI mods beyond the loader-owned ones while the loader is not OK.
func (ls *LaunchService) loaderPreflight(gameID string, mm *vfs.MountManager, profileName string) error {
	spec, _, ok := loaderSpecFor(gameID)
	if !ok || ls.s.svc.modLoader == nil {
		return nil
	}
	ls.s.mu.RLock()
	gameDir, err := ls.s.loaderGameDirLocked(gameID)
	state, hasState := ls.s.mountStates[gameID]
	ls.s.mu.RUnlock()
	if err != nil {
		return err
	}
	if err := loaderIntentRefusal(gameID, gameDir); err != nil {
		return err
	}
	mounted := mm.IsMounted()
	evaluated := profileName
	if mounted && hasState {
		evaluated = state.profileName
	}
	roots, err := ls.launchModRoots(gameID, mm, mounted, evaluated)
	if err != nil {
		return err
	}
	needed, err := smapiModsNeedLoader(roots, spec.BundledModIDs, loaderOwnedModFolders(spec.UninstallPaths))
	if err != nil {
		return fmt.Errorf("checking SMAPI mods before launch: %w", err)
	}
	if !needed {
		return nil
	}
	status, err := ls.s.svc.modLoader.engineFor(spec).Inspect(gameDir)
	if err != nil {
		return fmt.Errorf("checking SMAPI before launch: %w", err)
	}
	if status.State != smapi.StateOK {
		return &smapi.UnavailableError{GameID: gameID, State: status.State}
	}
	return nil
}

// launchModRoots returns the enabled mod folders of profileName plus Overwrite, and the game's base mods folder, that a launch deploys.
func (ls *LaunchService) launchModRoots(gameID string, mm *vfs.MountManager, mounted bool, profileName string) ([]string, error) {
	base := mm.DataPath()
	if mounted {
		base = mm.BackupPath()
	}
	if profileName == "" {
		return []string{base}, nil
	}
	_, entries, err := ls.s.profileMgr.Load(gameID, profileName)
	if err != nil {
		return nil, fmt.Errorf("loading profile %q for the SMAPI launch check: %w", profileName, err)
	}
	modsDir := config.ModsDir(gameID)
	guard := deployRootGuardFor(gameID)
	var roots []string
	for _, e := range entries {
		if !e.Enabled || e.Name == profile.OverwriteModName || download.ValidateTargetModName(e.Name) != nil {
			continue
		}
		modDir := filepath.Join(modsDir, e.Name)
		if guard != nil && guard(modDir) != "" {
			continue
		}
		roots = append(roots, modDir)
	}
	return append(roots, filepath.Join(modsDir, profile.OverwriteModName), base), nil
}

// loaderOwnedModFolders returns the Mods-relative folders, lower-cased, that the loader's uninstall paths claim under Mods/.
func loaderOwnedModFolders(uninstallPaths []string) []string {
	var folders []string
	for _, rel := range uninstallPaths {
		lower := strings.ToLower(rel)
		if rest, ok := strings.CutPrefix(lower, "mods/"); ok && rest != "" {
			folders = append(folders, rest)
		}
	}
	return folders
}

// isLoaderOwnedFolder reports whether the Mods-relative folder rel equals or lies beneath a loader-owned folder, ignoring case.
func isLoaderOwnedFolder(rel string, owned []string) bool {
	lower := strings.ToLower(filepath.ToSlash(rel))
	for _, folder := range owned {
		if lower == folder || strings.HasPrefix(lower, folder+"/") {
			return true
		}
	}
	return false
}

// smapiModsNeedLoader reports whether any root holds a SMAPI mod that is neither one the loader bundles nor in a loader-owned folder.
func smapiModsNeedLoader(roots, bundledIDs, ownedFolders []string) (bool, error) {
	for _, root := range roots {
		info, err := os.Stat(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if !info.IsDir() {
			continue
		}
		folders, err := smapi.ScanWith(root, smapi.ScanOptions{FollowSymlinks: true})
		if err != nil {
			return false, err
		}
		for _, folder := range folders {
			if folder.Kind != smapi.FolderMod || isLoaderOwnedFolder(folder.RelPath, ownedFolders) ||
				(folder.Manifest != nil && isBundledLoaderMod(folder.Manifest.UniqueID, bundledIDs)) {
				continue
			}
			return true, nil
		}
	}
	return false, nil
}

// isBundledLoaderMod reports whether uniqueID names one of the mods the loader installs itself.
func isBundledLoaderMod(uniqueID string, bundledIDs []string) bool {
	for _, id := range bundledIDs {
		if smapi.SameID(uniqueID, id) {
			return true
		}
	}
	return false
}

// launchStep runs the test hook for a named launch step.
func (s *session) launchStep(step string) error {
	if s.launchFault == nil {
		return nil
	}
	return s.launchFault(step)
}
