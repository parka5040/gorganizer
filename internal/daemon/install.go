package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
)

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
	if _, ok := is.s.config.Games[gameID]; !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	ch, _ := is.s.installBus.Subscribe(ctx, gameID)
	return ch, nil
}

// PreviewInstall extracts an archive into a daemon-cached tmpdir and returns a FOMOD plan or flat listing.
func (is *InstallService) PreviewInstall(gameID, archiveRelPath string) (*dto.PreviewResult, error) {
	if _, ok := is.s.config.Games[gameID]; !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	downloadsDir := config.DownloadsDir(gameID)
	absArchive := filepath.Join(downloadsDir, archiveRelPath)
	if _, err := os.Stat(absArchive); err != nil {
		return nil, &ArchiveMissingError{GameID: gameID, Path: archiveRelPath}
	}
	extractor, err := download.DetectExtractor(absArchive)
	if err != nil {
		return nil, fmt.Errorf("detecting archive type: %w", err)
	}
	tmp, err := os.MkdirTemp("", "gorganizer-preview-*")
	if err != nil {
		return nil, err
	}
	if err := extractor.Extract(absArchive, tmp); err != nil {
		os.RemoveAll(tmp)
		return nil, fmt.Errorf("extracting: %w", err)
	}
	download.ExpandNestedFomods(tmp)
	entry := &previewEntry{
		GameID: gameID, ArchiveRelPath: archiveRelPath, ExtractRoot: tmp,
	}
	out := &dto.PreviewResult{}
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

// DiscardPreview drops a cached preview's extraction.
func (is *InstallService) DiscardPreview(previewID string) error {
	if !is.s.previews.discard(previewID) {
		return &PreviewNotFoundError{PreviewID: previewID}
	}
	return nil
}

func (is *InstallService) StartInstall(req dto.StartInstallRequest) (string, int, error) {
	if _, ok := is.s.config.Games[req.GameID]; !ok {
		return "", 0, fmt.Errorf("%w: %s", config.ErrInvalidGameID, req.GameID)
	}
	if (req.ArchiveRelPath == "") == (req.ExternalArchivePath == "") {
		return "", 0, fmt.Errorf("exactly one of archive_rel_path or external_archive_path must be set")
	}

	var absArchive string
	var sidecar *download.ArchiveSidecar
	var indexRef download.SourceArchiveRef
	if req.ArchiveRelPath != "" {
		downloadsDir := config.DownloadsDir(req.GameID)
		absArchive = filepath.Join(downloadsDir, req.ArchiveRelPath)
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
		if target == "" && sidecar != nil {
			target = sidecar.ModName
		}
		if target == "" {
			base := filepath.Base(absArchive)
			target = strings.TrimSuffix(base, filepath.Ext(base))
		}
		target = download.SanitizeForFolder(target)
	}
	if target == "" {
		return "", 0, fmt.Errorf("could not determine target mod folder")
	}

	defer is.s.lockMods(req.GameID, target)()

	var extractedRoot string
	if req.PreviewID != "" {
		pe := is.s.previews.acquire(req.PreviewID)
		if pe == nil {
			return "", 0, &PreviewNotFoundError{PreviewID: req.PreviewID}
		}
		defer is.s.previews.release(req.PreviewID)
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
	if req.Mode == dto.InstallAsNewMod {
		is.s.svc.mods.ensureInModList(req.GameID, result.ModFolder)
	}

	if req.ArchiveRelPath != "" {
		if row, err := is.s.svc.archives.buildArchiveRow(req.GameID, req.ArchiveRelPath); err == nil {
			is.s.archiveBus.Publish(req.GameID, dto.ArchiveEventResult{
				GameID: req.GameID, RowChanged: row,
			})
		}
	}

	slog.Info("install complete",
		"game", req.GameID, "mod", result.ModFolder, "files", result.FileCount)
	return result.ModFolder, result.FileCount, nil
}
