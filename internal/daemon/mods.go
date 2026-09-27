package daemon

import (
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/mod"
	"github.com/parka/gorganizer/internal/profile"
)

// ListMods enumerates installed mods for a game.
func (md *ModService) ListMods(gameID string) ([]dto.ModInfoResult, error) {
	modsDir := config.ModsDir(gameID)
	mods, err := mod.ListMods(modsDir, gameID)
	if err != nil {
		return nil, err
	}
	var results []dto.ModInfoResult
	for _, m := range mods {
		results = append(results, dto.ModInfoResult{
			Name:      m.Name,
			GameID:    m.GameID,
			BasePath:  m.BasePath,
			FileCount: m.FileCount,
			TotalSize: m.TotalSize,
		})
	}
	return results, nil
}

// GetMod returns an info snapshot for a single mod folder.
func (md *ModService) GetMod(gameID, modName string) (*dto.ModInfoResult, error) {
	modDir, err := resolveModDir(gameID, modName)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(modDir); err != nil {
		if os.IsNotExist(err) {
			return nil, &ModNotFoundError{GameID: gameID, Name: modName}
		}
		return nil, err
	}
	m := mod.NewMod(modName, gameID, modDir)
	return &dto.ModInfoResult{
		Name:     m.Name,
		GameID:   m.GameID,
		BasePath: m.BasePath,
	}, nil
}

// RescanMod rewalks a mod folder and returns the full file list.
func (md *ModService) RescanMod(gameID, modName string) (*dto.ModInfoResult, error) {
	modDir, err := resolveModDir(gameID, modName)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(modDir); err != nil {
		if os.IsNotExist(err) {
			return nil, &ModNotFoundError{GameID: gameID, Name: modName}
		}
		return nil, err
	}
	m := mod.NewMod(modName, gameID, modDir)
	if err := m.Scan(); err != nil {
		return nil, err
	}
	return &dto.ModInfoResult{
		Name:      m.Name,
		GameID:    m.GameID,
		BasePath:  m.BasePath,
		FileCount: m.FileCount,
		TotalSize: m.TotalSize,
		Files:     m.Files,
	}, nil
}

// RenameMod renames a mod folder and its profile entries, rebuilding an active farm before returning.
func (md *ModService) RenameMod(gameID, oldName, newName string) error {
	if err := md.s.awaitRecovery(); err != nil {
		return err
	}
	if err := md.s.refuseWhenShuttingDown("rename_mod"); err != nil {
		return err
	}
	if !md.s.gameConfigured(gameID) {
		return fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	src, err := resolveExistingModDir(gameID, oldName)
	if err != nil {
		return err
	}
	if err := download.ValidateTargetModName(newName); err != nil {
		return err
	}
	if oldName == newName {
		return nil
	}
	defer md.s.lockMods(gameID, oldName, newName)()
	return md.renameModWithFarm(gameID, oldName, newName, src, filepath.Join(config.ModsDir(gameID), newName))
}

// renameModFolder renames a mod folder and its entry in every profile modlist, reporting whether the folder moved; the caller holds the profile lock.
func (md *ModService) renameModFolder(gameID, oldName, newName, src, dst string) (bool, error) {
	profiles, err := md.s.profileMgr.List(gameID)
	if err != nil {
		return false, fmt.Errorf("listing profiles: %w", err)
	}
	if err := requireRealModDir(gameID, oldName, src); err != nil {
		return false, err
	}
	if _, err := os.Lstat(dst); err == nil {
		return false, &ModCollisionError{Name: newName}
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("checking rename target: %w", err)
	}
	if err := os.Rename(src, dst); err != nil {
		return false, fmt.Errorf("renaming mod folder: %w", err)
	}

	return true, md.renameInModLists(gameID, profiles, oldName, newName)
}

// markMountedProfileDirty rebuilds the mounted profile's in-memory farm tree under the game's profile lock.
func (md *ModService) markMountedProfileDirty(gameID string) {
	defer md.s.lockProfiles(gameID)()
	md.s.mu.RLock()
	mm, mmOk := md.s.mountMgrs[gameID]
	ms, msOk := md.s.mountStates[gameID]
	gc, gcOk := md.s.config.Games[gameID]
	md.s.mu.RUnlock()
	if !mmOk || !msOk || !gcOk || !mm.IsMounted() {
		return
	}
	_, entries, err := md.s.profileMgr.Load(gameID, ms.profileName)
	if err != nil {
		slog.Warn("could not reload the mounted profile after a mod change", "game", gameID, "profile", ms.profileName, "err", err)
		return
	}
	layers := md.s.svc.vfs.buildLayers(gameID, gc, entries)
	if err := mm.MarkDirty(layers); err == nil {
		md.s.publishGuarded(dto.StatusEventResult{VFSStatus: md.s.svc.vfs.vfsStatus(gameID, gc, ms.profileName, mm, entries)})
	}
}

// renameInModLists renames a mod's entry in every listed profile modlist and returns the first failure; the caller holds the profile lock.
func (md *ModService) renameInModLists(gameID string, profiles []*profile.Profile, oldName, newName string) error {
	var firstErr error
	for _, p := range profiles {
		loaded, entries, err := md.s.profileMgr.Load(gameID, p.Name)
		if err != nil {
			slog.Warn("could not read modlist.txt while renaming a mod", "game", gameID, "profile", p.Name, "err", err)
			if firstErr == nil {
				firstErr = fmt.Errorf("loading profile %q: %w", p.Name, err)
			}
			continue
		}
		changed := false
		for i := range entries {
			if entries[i].Name == oldName {
				entries[i].Name = newName
				changed = true
			}
		}
		if !changed {
			continue
		}
		if err := md.s.profileMgr.Save(loaded, entries); err != nil {
			slog.Warn("could not update modlist.txt while renaming a mod", "game", gameID, "profile", p.Name, "err", err)
			if firstErr == nil {
				firstErr = fmt.Errorf("saving profile %q: %w", p.Name, err)
			}
		}
	}
	return firstErr
}

// dropFromModListsLocked removes a mod from all profiles; the caller holds the profile lock.
func (md *ModService) dropFromModListsLocked(gameID, modName string, force bool) error {
	profiles, err := md.s.profileMgr.List(gameID)
	if err != nil {
		return fmt.Errorf("listing profiles: %w", err)
	}
	var enabledIn []string
	for _, p := range profiles {
		_, entries, err := md.s.profileMgr.Load(gameID, p.Name)
		if err != nil {
			return fmt.Errorf("loading profile %q: %w", p.Name, err)
		}
		for _, e := range entries {
			if e.Name == modName && e.Enabled {
				enabledIn = append(enabledIn, p.Name)
				break
			}
		}
	}
	if len(enabledIn) > 0 && !force {
		return &ModInUseError{Name: modName, Profiles: enabledIn}
	}

	for _, p := range profiles {
		loaded, entries, err := md.s.profileMgr.Load(gameID, p.Name)
		if err != nil {
			return fmt.Errorf("loading profile %q: %w", p.Name, err)
		}
		kept := entries[:0]
		changed := false
		for _, e := range entries {
			if e.Name == modName {
				changed = true
				continue
			}
			kept = append(kept, e)
		}
		if !changed {
			continue
		}
		if err := md.s.profileMgr.Save(loaded, kept); err != nil {
			return fmt.Errorf("saving profile %q: %w", p.Name, err)
		}
	}
	return nil
}

// UninstallMod rebuilds an active farm without a mod before removing its folder and profile entries.
func (md *ModService) UninstallMod(gameID, modName string, force bool) ([]string, error) {
	if err := md.s.awaitRecovery(); err != nil {
		return nil, err
	}
	if err := md.s.refuseWhenShuttingDown("uninstall_mod"); err != nil {
		return nil, err
	}
	if !md.s.gameConfigured(gameID) {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	modDir, err := resolveExistingModDir(gameID, modName)
	if err != nil {
		return nil, err
	}
	defer md.s.lockMods(gameID, modName)()
	if err := requireRealModDir(gameID, modName, modDir); err != nil {
		return nil, err
	}
	meta, err := download.LoadModMetadata(modDir)
	if err != nil || meta == nil || (len(meta.SourceArchives) == 0 && meta.Name == "") {
		if _, statErr := os.Stat(modDir); os.IsNotExist(statErr) {
			return nil, &ModNotFoundError{GameID: gameID, Name: modName}
		}
		if err != nil {
			return nil, fmt.Errorf("reading mod metadata: %w", err)
		}
	}

	ownedSolely := map[string]bool{}
	if meta != nil {
		for _, sa := range meta.SourceArchives {
			ownedSolely[sa.Path] = true
		}
	}
	if len(ownedSolely) > 0 {
		modsDir := config.ModsDir(gameID)
		entries, _ := os.ReadDir(modsDir)
		for _, ent := range entries {
			if !ent.IsDir() || ent.Name() == "Downloads" || ent.Name() == modName {
				continue
			}
			other, err := download.LoadModMetadata(filepath.Join(modsDir, ent.Name()))
			if err != nil || other == nil {
				continue
			}
			for _, sa := range other.SourceArchives {
				if ownedSolely[sa.Path] {
					ownedSolely[sa.Path] = false
				}
			}
		}
	}

	applied, err := md.uninstallModWithFarm(gameID, modName, force)
	if err != nil {
		return nil, err
	}
	if err := removeModFolder(modDir); err != nil {
		return nil, err
	}

	var flagged []string
	for archivePath, solo := range ownedSolely {
		if !solo {
			continue
		}
		rel := strings.TrimPrefix(archivePath, "Downloads/")
		if err := download.SetUninstalled(gameID, rel, true); err != nil {
			slog.Warn("setting archive uninstalled flag failed", "path", archivePath, "err", err)
			continue
		}
		flagged = append(flagged, rel)
		if row, err := md.s.svc.archives.buildArchiveRow(gameID, rel); err == nil {
			md.s.archiveBus.Publish(gameID, dto.ArchiveEventResult{
				GameID: gameID, RowChanged: row,
			})
		}
	}

	md.s.invalidateInstalledArchiveCache(gameID)

	if !applied {
		md.markMountedProfileDirty(gameID)
	}
	slog.Info("mod uninstalled", "game", gameID, "mod", modName, "archives_flagged", flagged)
	return flagged, nil
}

// ensureInModList appends modName disabled to every profile modlist lacking it and returns the first failure.
func (md *ModService) ensureInModList(gameID, modName string) error {
	md.s.invalidateInstalledArchiveCache(gameID)
	_, err := md.appendToModLists(gameID, modName)
	return err
}

// appendToModLists appends modName disabled to every profile modlist lacking it, returning the updated count and first failure.
func (md *ModService) appendToModLists(gameID, modName string) (int, error) {
	defer md.s.lockProfiles(gameID)()
	profiles, err := md.s.profileMgr.List(gameID)
	if err != nil {
		return 0, fmt.Errorf("listing profiles: %w", err)
	}
	if len(profiles) == 0 {
		profiles = []*profile.Profile{{Name: "Default", GameID: gameID}}
	}
	updated := 0
	var firstErr error
	for _, p := range profiles {
		loaded, entries, err := md.s.profileMgr.Load(gameID, p.Name)
		if err != nil {
			slog.Warn("could not read modlist.txt", "game", gameID, "profile", p.Name, "err", err)
			if firstErr == nil {
				firstErr = fmt.Errorf("loading profile %q: %w", p.Name, err)
			}
			continue
		}
		if modListContains(entries, modName) {
			continue
		}
		entries = append(entries, mod.ModListEntry{Name: modName, Enabled: false})
		if err := md.s.profileMgr.Save(loaded, entries); err != nil {
			slog.Warn("could not update modlist.txt", "game", gameID, "profile", p.Name, "err", err)
			if firstErr == nil {
				firstErr = fmt.Errorf("saving profile %q: %w", p.Name, err)
			}
			continue
		}
		updated++
	}
	return updated, firstErr
}

// appendToProfileModList appends modName disabled to the existing profileName's modlist when it lacks it, never recreating a deleted profile.
func (md *ModService) appendToProfileModList(gameID, profileName, modName string) error {
	defer md.s.lockProfiles(gameID)()
	dir, err := md.s.profileMgr.CheckedProfileDir(gameID, profileName)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "profile.json")); err != nil {
		return fmt.Errorf("profile %q is not available: %w", profileName, err)
	}
	loaded, entries, err := md.s.profileMgr.Load(gameID, profileName)
	if err != nil {
		return fmt.Errorf("loading profile %q: %w", profileName, err)
	}
	if modListContains(entries, modName) {
		return nil
	}
	entries = append(entries, mod.ModListEntry{Name: modName, Enabled: false})
	if err := md.s.profileMgr.Save(loaded, entries); err != nil {
		return fmt.Errorf("saving profile %q: %w", profileName, err)
	}
	return nil
}

// modListContains reports whether entries already hold modName.
func modListContains(entries []mod.ModListEntry, modName string) bool {
	for _, e := range entries {
		if e.Name == modName {
			return true
		}
	}
	return false
}

// RegisterManualInstall is the post-install hook for paths that produce a mod folder without StartInstall, refusing once shutdown began.
func (md *ModService) RegisterManualInstall(gameID, modName, archiveRelPath string) (int, error) {
	if err := md.s.refuseWhenShuttingDown("register_install"); err != nil {
		return 0, err
	}
	modDir, err := md.manualInstallDir(gameID, modName)
	if err != nil {
		return 0, err
	}
	if err := validateModFolderLayout(gameID, modName, modDir); err != nil {
		return 0, err
	}
	return md.registerModFolder(gameID, modName, modDir, archiveRelPath)
}

// manualInstallDir validates a manual-install registration request and returns the existing mod folder.
func (md *ModService) manualInstallDir(gameID, modName string) (string, error) {
	if !md.s.gameConfigured(gameID) {
		return "", fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	if modName == "" {
		return "", fmt.Errorf("mod_name required")
	}
	if err := download.ValidateTargetModName(modName); err != nil {
		return "", err
	}
	modDir, err := resolveModDir(gameID, modName)
	if err != nil {
		return "", err
	}
	if err := requireRealModDir(gameID, modName, modDir); err != nil {
		return "", err
	}
	return modDir, nil
}

// registerModFolder appends an existing mod folder to every profile modlist and announces it as installed.
func (md *ModService) registerModFolder(gameID, modName, modDir, archiveRelPath string) (int, error) {
	md.s.invalidateInstalledArchiveCache(gameID)
	updated, err := md.appendToModLists(gameID, modName)
	if err != nil {
		return updated, &download.ModRegistrationError{Mod: modName, Err: err}
	}

	if archiveRelPath != "" {
		if row, err := md.s.svc.archives.buildArchiveRow(gameID, archiveRelPath); err == nil {
			md.s.archiveBus.Publish(gameID, dto.ArchiveEventResult{
				GameID: gameID, RowChanged: row,
			})
		}
	}

	fileCount := 0
	if meta, err := download.LoadModMetadata(modDir); err == nil && meta != nil {
		fileCount = meta.FileCount
	}
	md.s.installBus.Publish(gameID, dto.InstallEventResult{
		GameID: gameID,
		Progress: &dto.InstallProgressResult{
			InstallID:      "manual-" + modName,
			ArchiveRelPath: archiveRelPath,
			ModName:        modName,
			Step:           dto.InstallStepComplete,
			Pct:            100,
			FilesDone:      int64(fileCount),
			FilesTotal:     int64(fileCount),
			GameID:         gameID,
		},
	})

	return updated, nil
}

func (md *ModService) ListOverwriteFiles(gameID string) ([]dto.OverwriteEntryResult, string, error) {
	if !md.s.gameConfigured(gameID) {
		return nil, "", fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	owDir := filepath.Join(config.ModsDir(gameID), profile.OverwriteModName)
	var out []dto.OverwriteEntryResult
	err := filepath.WalkDir(owDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) && path == owDir {
				return filepath.SkipAll
			}
			return walkErr
		}
		if path == owDir {
			return nil
		}
		rel, _ := filepath.Rel(owDir, path)
		entry := dto.OverwriteEntryResult{
			RelPath: filepath.ToSlash(rel),
			IsDir:   d.IsDir(),
		}
		if info, ierr := d.Info(); ierr == nil {
			if !entry.IsDir {
				entry.SizeBytes = info.Size()
			}
			entry.ModifiedAt = info.ModTime().UTC().Format(time.RFC3339)
		}
		out = append(out, entry)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, owDir, err
	}
	return out, owDir, nil
}

// ExtractOverwriteToMod graduates a subset of loose files from Overwrite.
func (md *ModService) ExtractOverwriteToMod(gameID, modName string, files []string, keep bool) (int, error) {
	if !md.s.gameConfigured(gameID) {
		return 0, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	if modName == "" {
		return 0, fmt.Errorf("mod_name required")
	}
	if modName == profile.OverwriteModName {
		return 0, fmt.Errorf("mod_name %q is reserved", modName)
	}
	if err := download.ValidateTargetModName(modName); err != nil {
		return 0, err
	}

	modsDir := config.ModsDir(gameID)
	owDir := filepath.Join(modsDir, profile.OverwriteModName)
	destDir := filepath.Join(modsDir, modName)
	if _, err := os.Stat(destDir); err == nil {
		return 0, &ModCollisionError{Name: modName}
	}

	var paths []string
	if len(files) == 0 {
		_ = filepath.WalkDir(owDir, func(path string, de fs.DirEntry, err error) error {
			if err != nil || de.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(owDir, path)
			paths = append(paths, filepath.ToSlash(rel))
			return nil
		})
	} else {
		for _, f := range files {
			full := filepath.Join(owDir, filepath.FromSlash(f))
			info, err := os.Stat(full)
			if err != nil {
				continue
			}
			if !info.IsDir() {
				paths = append(paths, f)
				continue
			}
			_ = filepath.WalkDir(full, func(path string, de fs.DirEntry, werr error) error {
				if werr != nil || de.IsDir() {
					return werr
				}
				rel, _ := filepath.Rel(owDir, path)
				paths = append(paths, filepath.ToSlash(rel))
				return nil
			})
		}
	}
	if len(paths) == 0 {
		return 0, fmt.Errorf("no files to extract")
	}

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return 0, fmt.Errorf("creating mod dir: %w", err)
	}

	count := 0
	for _, rel := range paths {
		src := filepath.Join(owDir, filepath.FromSlash(rel))
		dst := filepath.Join(destDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			slog.Warn("mkdir for extract failed", "path", dst, "err", err)
			continue
		}
		if keep {
			if err := copyFileForExtract(src, dst); err != nil {
				slog.Warn("copy for extract failed", "src", src, "dst", dst, "err", err)
				continue
			}
		} else {
			if err := os.Rename(src, dst); err != nil {
				if err := copyFileForExtract(src, dst); err != nil {
					slog.Warn("move-via-copy for extract failed", "src", src, "err", err)
					continue
				}
				_ = os.Remove(src)
			}
		}
		count++
	}

	if !keep {
		var dirs []string
		_ = filepath.WalkDir(owDir, func(path string, de fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if de.IsDir() && path != owDir {
				dirs = append(dirs, path)
			}
			return nil
		})
		sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
		for _, d := range dirs {
			_ = os.Remove(d)
		}
	}

	if err := download.AppendSourceArchive(
		destDir, modName,
		download.SourceArchiveRef{},
		modName, "", "", "",
		paths,
	); err != nil {
		slog.Warn("ExtractOverwriteToMod: metadata write failed", "err", err)
	}

	if registeredDir, err := md.manualInstallDir(gameID, modName); err != nil {
		slog.Warn("ExtractOverwriteToMod: RegisterManualInstall failed", "err", err)
	} else if _, err := md.registerModFolder(gameID, modName, registeredDir, ""); err != nil {
		slog.Warn("ExtractOverwriteToMod: RegisterManualInstall failed", "err", err)
	}

	return count, nil
}

func copyFileForExtract(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}
