package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/fsutil"
)

const extractionPrefix = "gorganizer-preview-*"

func (is *InstallService) runPreviewSweeper() {
	t := time.NewTicker(2 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-is.s.shutdownCh:
			return
		case <-t.C:
			is.s.previews.sweep()
		}
	}
}

// StreamInstallEvents subscribes the caller to per-game install progress.
func (is *InstallService) StreamInstallEvents(ctx context.Context, gameID string) (<-chan dto.InstallEventResult, error) {
	if !is.s.gameConfigured(gameID) {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	ch, _ := is.s.installBus.Subscribe(ctx, gameID)
	return ch, nil
}

// PreviewInstall extracts an archive into a daemon-cached tmpdir and returns a FOMOD plan or flat listing.
func (is *InstallService) PreviewInstall(req dto.PreviewInstallRequest) (*dto.PreviewResult, error) {
	gameID, archiveRelPath := req.GameID, req.ArchiveRelPath
	if req.ExternalArchivePath != "" {
		return nil, fmt.Errorf("previewing an archive outside the Downloads folder is not supported yet")
	}
	if !is.s.gameConfigured(gameID) {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	if err := checkInstallLayout(gameID); err != nil {
		return nil, err
	}
	planner := layoutPlannerFor(gameID)
	downloadsDir := config.DownloadsDir(gameID)
	absArchive, err := archivePath(downloadsDir, archiveRelPath)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(absArchive); err != nil {
		return nil, &ArchiveMissingError{GameID: gameID, Path: archiveRelPath}
	}
	tmp, err := extractArchive(absArchive)
	if err != nil {
		return nil, err
	}
	entry := &previewEntry{
		GameID: gameID, ArchiveRelPath: archiveRelPath, ExtractRoot: tmp,
	}
	out := &dto.PreviewResult{}
	if planner != nil {
		files, err := plannedPreviewFiles(planner, tmp)
		if err != nil {
			os.RemoveAll(tmp)
			return nil, err
		}
		out.FlatFileList = files
		out.PreviewID = is.s.previews.put(entry)
		return out, nil
	}
	download.ExpandNestedFomods(tmp)
	if root, kind := download.FindFomodRootKind(tmp); kind != download.FomodKindNone {
		entry.HasFomod = true
		entry.ModuleRoot = root
		out.HasFomod = true
		switch kind {
		case download.FomodKindModuleConfig:
			out.Plan = &dto.FomodPlanResult{
				ModuleName: filepath.Base(archiveRelPath),
				ModulePath: root,
			}
		case download.FomodKindLegacyInfoOnly:
			info := download.ParseLegacyFomodInfo(root)
			out.Plan = &dto.FomodPlanResult{
				ModuleName:     info.Name,
				ModulePath:     root,
				LegacyInfoOnly: true,
				Description:    info.Description,
				ScreenshotPath: info.ScreenshotPath,
				Version:        info.Version,
				Author:         info.Author,
			}
		}
	} else {
		contentRoot := download.FindContentRoot(tmp)
		_ = filepath.WalkDir(contentRoot, func(path string, de os.DirEntry, err error) error {
			if err != nil || de.IsDir() {
				return nil
			}
			rel, rerr := filepath.Rel(contentRoot, path)
			if rerr == nil {
				out.FlatFileList = append(out.FlatFileList, filepath.ToSlash(rel))
			}
			return nil
		})
	}
	out.PreviewID = is.s.previews.put(entry)
	return out, nil
}

// extractArchive extracts absArchive into a fresh directory under the daemon's extraction root that the caller must remove.
func extractArchive(absArchive string) (string, error) {
	extractor, err := download.DetectExtractor(absArchive)
	if err != nil {
		return "", fmt.Errorf("detecting archive type: %w", err)
	}
	root, err := extractionRoot()
	if err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(root, extractionPrefix)
	if err != nil {
		return "", err
	}
	if err := extractor.Extract(absArchive, tmp); err != nil {
		os.RemoveAll(tmp)
		return "", fmt.Errorf("extracting: %w", err)
	}
	return tmp, nil
}

// extractionRoot returns, creating it, the private parent of this daemon instance's archive extractions under the temporary directory, keyed by the runtime directory whose lock admits one daemon, and refuses a path another user or a symlink holds.
func extractionRoot() (string, error) {
	sum := sha256.Sum256([]byte(config.RuntimeDir()))
	root := filepath.Join(os.TempDir(), fmt.Sprintf("gorganizer-extract-%d-%s", os.Getuid(), hex.EncodeToString(sum[:6])))
	if err := os.Mkdir(root, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("creating the extraction root: %w", err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return "", fmt.Errorf("checking the extraction root: %w", err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || int(st.Uid) != os.Getuid() || info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("extraction root %s is not a private directory of this user", root)
	}
	return root, nil
}

// sweepStaleExtractions removes the archive extractions an earlier daemon instance left in this instance's extraction root, keeping any made since this daemon started.
func (s *session) sweepStaleExtractions() {
	root, err := extractionRoot()
	if err != nil {
		slog.Warn("sweeping stale archive extractions skipped", "err", err)
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		slog.Warn("sweeping stale archive extractions failed", "path", root, "err", err)
		return
	}
	cutoff := s.startedAt.Add(-stageSweepMargin)
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			slog.Warn("removing a stale archive extraction failed", "path", path, "err", err)
			continue
		}
		slog.Info("removed a stale archive extraction", "path", path)
	}
}

// plannedPreviewFiles lists every regular file of each planned source as DestName/<rel>, sorted.
func plannedPreviewFiles(planner download.LayoutPlanner, extractRoot string) ([]string, error) {
	copies, err := planner.Plan(extractRoot)
	if err != nil {
		return nil, err
	}
	if err := download.ValidatePlannedCopies(copies); err != nil {
		return nil, err
	}
	var files []string
	for _, planned := range copies {
		source := extractRoot
		if planned.SourceRel != "" {
			joined, err := fsutil.SafeJoin(extractRoot, planned.SourceRel, true)
			if err != nil {
				return nil, fmt.Errorf("%w: planned source %q: %v", download.ErrUnsafeArchive, planned.SourceRel, err)
			}
			source = joined
		}
		err := filepath.WalkDir(source, func(path string, de os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !de.Type().IsRegular() {
				return nil
			}
			rel, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			files = append(files, planned.DestName+"/"+filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("listing planned source %q: %w", planned.SourceRel, err)
		}
	}
	sort.Strings(files)
	return files, nil
}

// previewMatchesRequest refuses a cached preview that was extracted for another game or another archive than the install request names.
func previewMatchesRequest(pe *previewEntry, req dto.StartInstallRequest, absArchive string) error {
	mismatch := &PreviewNotFoundError{PreviewID: req.PreviewID}
	if pe.GameID != req.GameID || req.ArchiveRelPath == "" {
		return fmt.Errorf("%w: preview was extracted for game %q archive %q", mismatch, pe.GameID, pe.ArchiveRelPath)
	}
	previewArchive, err := archivePath(config.DownloadsDir(pe.GameID), pe.ArchiveRelPath)
	if err != nil || previewArchive != absArchive {
		return fmt.Errorf("%w: preview was extracted for archive %q", mismatch, pe.ArchiveRelPath)
	}
	return nil
}

// DiscardPreview drops a cached preview's extraction.
func (is *InstallService) DiscardPreview(previewID string) error {
	if !is.s.previews.discard(previewID) {
		return &PreviewNotFoundError{PreviewID: previewID}
	}
	return nil
}

// StartInstall installs an archive and publishes InstallCompleted once the install and its modlist registration succeeded.
func (is *InstallService) StartInstall(req dto.StartInstallRequest) (string, int, error) {
	folder, count, err := is.startInstallFrom(req, "")
	if err == nil {
		is.publishInstallCompleted(req.GameID, folder, req.ArchiveRelPath, nil)
	}
	return folder, count, err
}

// publishInstallCompleted announces a registered install on the game's install stream as a lossy refresh hint naming every dependency batch it satisfied.
func (is *InstallService) publishInstallCompleted(gameID, modName, archiveRelPath string, batchIDs []string) {
	completed := &dto.InstallCompletedResult{
		GameID: gameID, ModName: modName, ArchiveRelPath: archiveRelPath, BatchIDs: append([]string(nil), batchIDs...),
	}
	if len(batchIDs) > 0 {
		completed.BatchID = batchIDs[0]
	}
	is.s.installBus.Publish(gameID, dto.InstallEventResult{GameID: gameID, Completed: completed})
}

// gameConfigured reports whether gameID is configured, reading the config under s.mu.
func (s *session) gameConfigured(gameID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.config.Games[gameID]
	return ok
}

// ensureModsDir creates gameID's mod store when it is missing and refuses a store path that is not a directory.
func ensureModsDir(gameID string) error {
	modsDir := config.ModsDir(gameID)
	info, err := os.Stat(modsDir)
	switch {
	case err == nil && !info.IsDir():
		return fmt.Errorf("mods directory %s is not a directory", modsDir)
	case err == nil:
		return nil
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("checking mods directory: %w", err)
	}
	if err := os.MkdirAll(modsDir, 0o755); err != nil {
		return fmt.Errorf("creating mods directory: %w", err)
	}
	return nil
}

// startInstallFrom waits for startup recovery, then extracts (unless extracted names a checked extraction of the archive), stages and registers an archive install without announcing its completion, refusing once shutdown began.
func (is *InstallService) startInstallFrom(req dto.StartInstallRequest, extracted string) (string, int, error) {
	if err := is.s.awaitRecovery(); err != nil {
		return "", 0, err
	}
	if err := is.s.refuseWhenShuttingDown("install"); err != nil {
		return "", 0, err
	}
	if !is.s.gameConfigured(req.GameID) {
		return "", 0, fmt.Errorf("%w: %s", config.ErrInvalidGameID, req.GameID)
	}
	if err := checkInstallLayout(req.GameID); err != nil {
		return "", 0, err
	}
	if (req.ArchiveRelPath == "") == (req.ExternalArchivePath == "") {
		return "", 0, fmt.Errorf("exactly one of archive_rel_path or external_archive_path must be set")
	}

	var absArchive string
	var sidecar *download.ArchiveSidecar
	var indexRef download.SourceArchiveRef
	if req.ArchiveRelPath != "" {
		downloadsDir := config.DownloadsDir(req.GameID)
		var err error
		absArchive, err = archivePath(downloadsDir, req.ArchiveRelPath)
		if err != nil {
			return "", 0, err
		}
		if _, err := os.Stat(absArchive); err != nil {
			return "", 0, &ArchiveMissingError{GameID: req.GameID, Path: req.ArchiveRelPath}
		}
		sidecar, _ = download.LoadSidecar(absArchive)
		indexRef.Path = filepath.Join("Downloads", req.ArchiveRelPath)
	} else {
		absArchive = req.ExternalArchivePath
		if _, err := os.Stat(absArchive); err != nil {
			return "", 0, fmt.Errorf("external archive not found: %w", err)
		}
		indexRef.Path = absArchive
	}
	if sidecar != nil {
		indexRef.ModID = sidecar.ModID
		indexRef.FileID = sidecar.FileID
	}

	target := req.TargetMod
	if req.Mode == dto.InstallAsNewMod {
		derived := target == ""
		if derived && sidecar != nil {
			target = sidecar.ModName
		}
		if target == "" {
			base := filepath.Base(absArchive)
			target = strings.TrimSuffix(base, filepath.Ext(base))
		}
		target = download.SanitizeForFolder(target)
		if derived {
			target = download.NormalizeDerivedModName(target)
		}
	}
	if target == "" {
		return "", 0, fmt.Errorf("could not determine target mod folder")
	}
	if err := download.ValidateTargetModName(target); err != nil {
		return "", 0, err
	}

	defer is.s.lockMods(req.GameID, target)()
	if err := ensureModsDir(req.GameID); err != nil {
		return "", 0, err
	}
	if req.Mode == dto.InstallMergeIntoMod {
		if err := download.ValidateMergeTarget(config.ModsDir(req.GameID), target); err != nil {
			return "", 0, err
		}
	}

	extractedRoot := extracted
	if req.PreviewID != "" {
		pe := is.s.previews.acquire(req.PreviewID)
		if pe == nil {
			return "", 0, &PreviewNotFoundError{PreviewID: req.PreviewID}
		}
		defer is.s.previews.release(req.PreviewID)
		if err := previewMatchesRequest(pe, req, absArchive); err != nil {
			return "", 0, err
		}
		extractedRoot = pe.ExtractRoot
		if len(req.FomodSelectedFiles) > 0 && pe.ModuleRoot != "" {
			extractedRoot = pe.ModuleRoot
		}
	}

	sink := func(p download.InstallProgress) {
		is.s.installBus.Publish(req.GameID, dto.InstallEventResult{
			GameID: req.GameID,
			Progress: &dto.InstallProgressResult{
				InstallID:      p.InstallID,
				ArchiveRelPath: req.ArchiveRelPath,
				ModName:        target,
				Step:           dto.InstallStep(p.Step),
				Pct:            p.Pct,
				CurrentFile:    p.CurrentFile,
				FilesDone:      p.FilesDone,
				FilesTotal:     p.FilesTotal,
				Error:          p.Error,
				GameID:         req.GameID,
			},
		})
	}

	var fomodFiles []download.FomodFile
	for _, f := range req.FomodSelectedFiles {
		fomodFiles = append(fomodFiles, download.FomodFile{
			Source: f.Source, Destination: f.Destination,
			IsFolder: f.IsFolder, Priority: f.Priority,
		})
	}

	installReq := download.InstallRequest{
		GameID:             req.GameID,
		ArchivePath:        absArchive,
		ExtractedRoot:      extractedRoot,
		Mode:               download.InstallMode(req.Mode),
		TargetMod:          target,
		SourceArchiveRef:   indexRef,
		FomodSelectedFiles: fomodFiles,
		ProgressSink:       sink,
		Layout:             layoutPlannerFor(req.GameID),
	}
	if sidecar != nil {
		installReq.DisplayName = sidecar.ModName
		installReq.Category = sidecar.Category
		installReq.Version = sidecar.Version
		if sidecar.GameDomain != "" && sidecar.ModID > 0 {
			installReq.ModPage = fmt.Sprintf("https://www.nexusmods.com/%s/mods/%d",
				sidecar.GameDomain, sidecar.ModID)
		}
	}

	result, err := download.Install(installReq)
	if err != nil {
		if path, ok := download.IsFomodMarker(err); ok {
			return "", 0, &FomodRequiredError{
				GameID: req.GameID, Path: path, PreviewID: req.PreviewID,
			}
		}
		if name, ok := download.IsCollisionMarker(err); ok {
			return "", 0, &ModCollisionError{Name: name}
		}
		return "", 0, err
	}

	if req.PreviewID != "" {
		is.s.previews.discard(req.PreviewID)
	}

	is.s.invalidateInstalledArchiveCache(req.GameID)
	var registrationErr error
	if req.Mode == dto.InstallAsNewMod {
		if err := is.s.svc.mods.ensureInModList(req.GameID, result.ModFolder); err != nil {
			registrationErr = &download.ModRegistrationError{Mod: result.ModFolder, Err: err}
		}
	}

	if req.ArchiveRelPath != "" {
		if row, err := is.s.svc.archives.buildArchiveRow(req.GameID, req.ArchiveRelPath); err == nil {
			is.s.archiveBus.Publish(req.GameID, dto.ArchiveEventResult{
				GameID: req.GameID, RowChanged: row,
			})
		}
	}

	if registrationErr != nil {
		slog.Warn("install registration failed",
			"game", req.GameID, "mod", result.ModFolder, "err", registrationErr)
		return result.ModFolder, result.FileCount, registrationErr
	}
	slog.Info("install complete",
		"game", req.GameID, "mod", result.ModFolder, "files", result.FileCount)
	return result.ModFolder, result.FileCount, nil
}
