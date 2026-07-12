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
	modDir := filepath.Join(config.ModsDir(gameID), modName)
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
	modDir := filepath.Join(config.ModsDir(gameID), modName)
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

// RenameMod atomically renames a mod folder and updates every profile's modlist.txt.
func (md *ModService) RenameMod(gameID, oldName, newName string) error {
	if _, ok := md.s.config.Games[gameID]; !ok {
		return fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	if oldName == newName {
		return nil
	}
	defer md.s.lockMods(gameID, oldName, newName)()
	modsDir := config.ModsDir(gameID)
	src := filepath.Join(modsDir, oldName)
	dst := filepath.Join(modsDir, newName)
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return &ModNotFoundError{GameID: gameID, Name: oldName}
		}
		return err
	}
	if _, err := os.Stat(dst); err == nil {
		return &ModCollisionError{Name: newName}
	}
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("renaming mod folder: %w", err)
	}

	meta, _ := download.LoadModMetadata(dst)
	if meta != nil {
		meta.Folder = newName
		if meta.Name == oldName {
			meta.Name = newName
		}
		_ = download.SaveModMetadata(dst, meta)
	}

	profiles, _ := md.s.profileMgr.List(gameID)
	for _, p := range profiles {
		_, entries, err := md.s.profileMgr.Load(gameID, p.Name)
		if err != nil {
			continue
		}
		changed := false
		for i := range entries {
			if entries[i].Name == oldName {
				entries[i].Name = newName
				changed = true
			}
		}
		if changed {
			_ = md.s.profileMgr.Save(p, entries)
		}
	}

	md.s.invalidateInstalledArchiveCache(gameID)
	md.s.mu.RLock()
	mm, mmOk := md.s.mountMgrs[gameID]
	ms, msOk := md.s.mountStates[gameID]
	gc, gcOk := md.s.config.Games[gameID]
	md.s.mu.RUnlock()
	if mmOk && msOk && gcOk && mm.IsMounted() {
		if _, entries, err := md.s.profileMgr.Load(gameID, ms.profileName); err == nil {
			layers := md.s.svc.vfs.buildLayers(gameID, gc, entries)
			if err := mm.MarkDirty(layers); err == nil {
				select {
				case md.s.statusCh <- dto.StatusEventResult{VFSStatus: md.s.svc.vfs.vfsStatus(gameID, gc, ms.profileName, mm, entries)}:
				default:
				}
			}
		}
	}
	return nil
}

// UninstallModAsync is the async wrapper used for huge mod folders.
func (md *ModService) UninstallModAsync(gameID, modName string, force bool) ([]string, error) {
	flagged, modDir, err := md.uninstallModSync(gameID, modName, force)
	if err != nil || modDir == "" {
		return flagged, err
	}
	go func() {
		var removeErr error
		for attempt := 0; attempt < 2; attempt++ {
			if rerr := os.RemoveAll(modDir); rerr == nil {
				removeErr = nil
				break
			} else {
				removeErr = rerr
				time.Sleep(100 * time.Millisecond)
			}
		}
		if removeErr != nil {
			slog.Warn("UninstallModAsync: removeall failed", "mod", modName, "err", removeErr)
			return
		}
		slog.Info("UninstallModAsync: removeall complete", "mod", modName)
		md.s.invalidateInstalledArchiveCache(gameID)
	}()
	return flagged, nil
}

// uninstallModSync is the shared bookkeeping path for UninstallMod and UninstallModAsync.
func (md *ModService) uninstallModSync(gameID, modName string, force bool) ([]string, string, error) {
	if _, ok := md.s.config.Games[gameID]; !ok {
		return nil, "", fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	modDir := filepath.Join(config.ModsDir(gameID), modName)
	meta, err := download.LoadModMetadata(modDir)
	if err != nil || meta == nil || (len(meta.SourceArchives) == 0 && meta.Name == "") {
		if _, statErr := os.Stat(modDir); os.IsNotExist(statErr) {
			return nil, "", &ModNotFoundError{GameID: gameID, Name: modName}
		}
		if err != nil {
			return nil, "", fmt.Errorf("reading mod metadata: %w", err)
		}
	}

	profiles, _ := md.s.profileMgr.List(gameID)
	var enabledIn []string
	for _, p := range profiles {
		_, entries, err := md.s.profileMgr.Load(gameID, p.Name)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.Name == modName && e.Enabled {
				enabledIn = append(enabledIn, p.Name)
				break
			}
		}
	}
	if len(enabledIn) > 0 && !force {
		return nil, "", &ModInUseError{Name: modName, Profiles: enabledIn}
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

	for _, p := range profiles {
		_, entries, err := md.s.profileMgr.Load(gameID, p.Name)
		if err != nil {
			continue
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
		if changed {
			_ = md.s.profileMgr.Save(p, kept)
		}
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

	return flagged, modDir, nil
}

// UninstallMod removes a mod's install dir and strips it from every profile.
func (md *ModService) UninstallMod(gameID, modName string, force bool) ([]string, error) {
	if _, ok := md.s.config.Games[gameID]; !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	defer md.s.lockMods(gameID, modName)()
	modDir := filepath.Join(config.ModsDir(gameID), modName)
	meta, err := download.LoadModMetadata(modDir)
	if err != nil || meta == nil || (len(meta.SourceArchives) == 0 && meta.Name == "") {
		if _, statErr := os.Stat(modDir); os.IsNotExist(statErr) {
			return nil, &ModNotFoundError{GameID: gameID, Name: modName}
		}
		if err != nil {
			return nil, fmt.Errorf("reading mod metadata: %w", err)
		}
	}

	profiles, _ := md.s.profileMgr.List(gameID)
	var enabledIn []string
	for _, p := range profiles {
		_, entries, err := md.s.profileMgr.Load(gameID, p.Name)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.Name == modName && e.Enabled {
				enabledIn = append(enabledIn, p.Name)
				break
			}
		}
	}
	if len(enabledIn) > 0 && !force {
		return nil, &ModInUseError{Name: modName, Profiles: enabledIn}
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

	for _, p := range profiles {
		_, entries, err := md.s.profileMgr.Load(gameID, p.Name)
		if err != nil {
			continue
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
		if changed {
			_ = md.s.profileMgr.Save(p, kept)
		}
	}

	var removeErr error
	for attempt := 0; attempt < 2; attempt++ {
		if err := os.RemoveAll(modDir); err == nil {
			removeErr = nil
			break
		} else {
			removeErr = err
			time.Sleep(100 * time.Millisecond)
		}
	}
	if removeErr != nil {
		return nil, fmt.Errorf("removing mod folder: %w", removeErr)
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

	md.s.mu.RLock()
	mm, mmOk := md.s.mountMgrs[gameID]
	ms, msOk := md.s.mountStates[gameID]
	gc, gcOk := md.s.config.Games[gameID]
	md.s.mu.RUnlock()
	if mmOk && msOk && gcOk && mm.IsMounted() {
		if _, entries, err := md.s.profileMgr.Load(gameID, ms.profileName); err == nil {
			layers := md.s.svc.vfs.buildLayers(gameID, gc, entries)
			if err := mm.MarkDirty(layers); err == nil {
				select {
				case md.s.statusCh <- dto.StatusEventResult{VFSStatus: md.s.svc.vfs.vfsStatus(gameID, gc, ms.profileName, mm, entries)}:
				default:
				}
			}
		}
	}
	slog.Info("mod uninstalled", "game", gameID, "mod", modName, "archives_flagged", flagged)
	return flagged, nil
}

func (md *ModService) ReinstallMod(gameID, modName string) (int, int, int, error) {
	if _, ok := md.s.config.Games[gameID]; !ok {
		return 0, 0, 0, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	defer md.s.lockMods(gameID, modName)()
	modsDir := config.ModsDir(gameID)
	modDir := filepath.Join(modsDir, modName)
	meta, err := download.LoadModMetadata(modDir)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("loading mod metadata: %w", err)
	}
	if meta == nil || len(meta.SourceArchives) == 0 {
		return 0, 0, 0, fmt.Errorf("mod %q has no source_archives to replay", modName)
	}

	if err := download.ClearModFiles(modDir); err != nil {
		return 0, 0, 0, fmt.Errorf("clearing mod files: %w", err)
	}
	preserved := *meta
	preserved.SourceArchives = nil
	preserved.Files = nil
	preserved.FileCount = 0
	_ = download.SaveModMetadata(modDir, &preserved)

	var replayed, skipped int
	for _, sa := range meta.SourceArchives {
		abs := filepath.Join(modsDir, sa.Path)
		if _, err := os.Stat(abs); err != nil {
			slog.Warn("reinstall: archive missing, skipping", "path", sa.Path)
			skipped++
			continue
		}
		sink := func(p download.InstallProgress) {
			md.s.installBus.Publish(gameID, dto.InstallEventResult{
				GameID: gameID,
				Progress: &dto.InstallProgressResult{
					InstallID: p.InstallID, ModName: modName,
					Step: dto.InstallStep(p.Step), Pct: p.Pct,
					CurrentFile: p.CurrentFile, FilesDone: p.FilesDone,
					FilesTotal: p.FilesTotal, Error: p.Error, GameID: gameID,
				},
			})
		}
		req := download.InstallRequest{
			GameID: gameID, ArchivePath: abs,
			Mode: download.ModeMergeIntoMod, TargetMod: modName,
			SourceArchiveRef: download.SourceArchiveRef{
				Path: sa.Path, ModID: sa.ModID, FileID: sa.FileID,
				InstalledAt: sa.InstalledAt,
			},
			ProgressSink: sink,
		}
		if _, err := download.Install(req); err != nil {
			slog.Error("reinstall step failed", "archive", sa.Path, "err", err)
			skipped++
			continue
		}
		replayed++
	}

	final, _ := download.LoadModMetadata(modDir)
	fileCount := 0
	if final != nil {
		fileCount = final.FileCount
	}
	md.s.invalidateInstalledArchiveCache(gameID)
	return replayed, skipped, fileCount, nil
}

func (md *ModService) ensureInModList(gameID, modName string) {
	md.s.invalidateInstalledArchiveCache(gameID)
	profiles, err := md.s.profileMgr.List(gameID)
	if err != nil || len(profiles) == 0 {
		profiles = []*profile.Profile{{Name: "Default", GameID: gameID}}
	}
	for _, p := range profiles {
		_, entries, err := md.s.profileMgr.Load(gameID, p.Name)
		if err != nil {
			entries = nil
		}
		present := false
		for _, e := range entries {
			if e.Name == modName {
				present = true
				break
			}
		}
		if present {
			continue
		}
		entries = append(entries, mod.ModListEntry{Name: modName, Enabled: false})
		if err := md.s.profileMgr.Save(p, entries); err != nil {
			slog.Warn("could not update modlist.txt", "game", gameID, "profile", p.Name, "err", err)
		}
	}
}

// RegisterManualInstall is the post-install hook for paths that produce a mod folder without StartInstall.
func (md *ModService) RegisterManualInstall(gameID, modName, archiveRelPath string) (int, error) {
	if _, ok := md.s.config.Games[gameID]; !ok {
		return 0, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	if modName == "" {
		return 0, fmt.Errorf("mod_name required")
	}
	modDir := filepath.Join(config.ModsDir(gameID), modName)
	if _, err := os.Stat(modDir); err != nil {
		if os.IsNotExist(err) {
			return 0, &ModNotFoundError{GameID: gameID, Name: modName}
		}
		return 0, err
	}

	md.s.invalidateInstalledArchiveCache(gameID)

	profiles, err := md.s.profileMgr.List(gameID)
	if err != nil || len(profiles) == 0 {
		profiles = []*profile.Profile{{Name: "Default", GameID: gameID}}
	}
	updated := 0
	for _, p := range profiles {
		_, entries, err := md.s.profileMgr.Load(gameID, p.Name)
		if err != nil {
			entries = nil
		}
		present := false
		for _, e := range entries {
			if e.Name == modName {
				present = true
				break
			}
		}
		if present {
			continue
		}
		entries = append(entries, mod.ModListEntry{Name: modName, Enabled: false})
		if err := md.s.profileMgr.Save(p, entries); err != nil {
			slog.Warn("RegisterManualInstall: could not update modlist.txt",
				"game", gameID, "profile", p.Name, "err", err)
			continue
		}
		updated++
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
	if _, ok := md.s.config.Games[gameID]; !ok {
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
	if _, ok := md.s.config.Games[gameID]; !ok {
		return 0, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	if modName == "" {
		return 0, fmt.Errorf("mod_name required")
	}
	if modName == profile.OverwriteModName {
		return 0, fmt.Errorf("mod_name %q is reserved", modName)
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

	if _, err := md.RegisterManualInstall(gameID, modName, ""); err != nil {
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
