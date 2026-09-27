package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/fsutil"
)

const (
	extractionPrefix    = "gorganizer-preview-*"
	maxPreviewFlatFiles = 2000
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
	if !is.s.gameConfigured(gameID) {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	ch, _ := is.s.installBus.Subscribe(ctx, gameID)
	return ch, nil
}

// PreviewInstall extracts an archive into a daemon-cached tmpdir and returns a FOMOD plan or flat listing.
func (is *InstallService) PreviewInstall(req dto.PreviewInstallRequest) (*dto.PreviewResult, error) {
	gameID, archiveRelPath := req.GameID, req.ArchiveRelPath
	if (archiveRelPath == "") == (req.ExternalArchivePath == "") {
		return nil, fmt.Errorf("exactly one of archive_rel_path or external_archive_path must be set")
	}
	if !is.s.gameConfigured(gameID) {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	if err := checkInstallLayout(gameID); err != nil {
		return nil, err
	}
	planner := layoutPlannerFor(gameID)
	entry := &previewEntry{GameID: gameID, ArchiveRelPath: archiveRelPath}
	var absArchive string
	if archiveRelPath != "" {
		var err error
		absArchive, err = archivePath(config.DownloadsDir(gameID), archiveRelPath)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(absArchive); err != nil {
			return nil, &ArchiveMissingError{GameID: gameID, Path: archiveRelPath}
		}
	} else {
		var err error
		absArchive, entry.ExternalIdentity, err = resolveExternalArchive(req.ExternalArchivePath)
		if err != nil {
			return nil, err
		}
		entry.ExternalArchivePath = absArchive
	}
	budget := download.NewExtractBudget()
	tmp, err := extractArchiveWithBudget(absArchive, budget)
	if err != nil {
		return nil, err
	}
	entry.ExtractRoot = tmp
	cached := false
	defer func() {
		if !cached {
			os.RemoveAll(tmp)
		}
	}()
	if req.ExternalArchivePath != "" {
		resolved, identity, err := resolveExternalArchive(req.ExternalArchivePath)
		if err != nil || resolved != entry.ExternalArchivePath || identity != entry.ExternalIdentity {
			return nil, &PreviewNotFoundError{}
		}
	}
	out := &dto.PreviewResult{}
	if planner != nil {
		files, err := plannedPreviewFiles(planner, tmp)
		if err != nil {
			return nil, err
		}
		out.FlatFileList = files[:min(len(files), maxPreviewFlatFiles)]
		out.PreviewID = is.s.previews.put(entry)
		cached = true
		return out, nil
	}
	if err := download.ExpandNestedFomods(tmp, budget); err != nil {
		return nil, fmt.Errorf("expanding nested installers: %w", err)
	}
	if root, kind := download.FindFomodRootKind(tmp); kind != download.FomodKindNone {
		entry.HasFomod = true
		entry.ModuleRoot = root
		out.HasFomod = true
		switch kind {
		case download.FomodKindModuleConfig:
			xmlBytes, err := moduleConfigBytes(root)
			if err != nil {
				return nil, err
			}
			name := filepath.Base(absArchive)
			entry.RequiredFiles = requiredFomodFiles(xmlBytes)
			out.Plan = &dto.FomodPlanResult{
				ModuleName: name, ModulePath: root, ModuleConfigXML: xmlBytes,
				RequiredFiles: entry.RequiredFiles,
			}
			var doc struct {
				Image struct {
					Path string `xml:"path,attr"`
				} `xml:"moduleImage"`
			}
			if xml.Unmarshal(xmlBytes, &doc) == nil && doc.Image.Path != "" {
				out.Plan.ScreenshotData = previewScreenshotBytes(tmp, root, doc.Image.Path)
			}
		case download.FomodKindLegacyInfoOnly:
			entry.LegacyInfoOnly = true
			info := download.ParseLegacyFomodInfo(root)
			out.Plan = &dto.FomodPlanResult{
				ModuleName:     info.Name,
				ModulePath:     root,
				LegacyInfoOnly: true,
				Description:    info.Description,
				ScreenshotPath: info.ScreenshotPath,
				ScreenshotData: previewScreenshotBytes(tmp, root, info.ScreenshotPath),
				Version:        info.Version,
				Author:         info.Author,
			}
		}
	} else {
		rel, ambiguous := download.DetectContentRoot(tmp, gameID)
		entry.DetectedRoot = rel
		out.DetectedRoot = rel
		out.RootAmbiguous = ambiguous
		roots, err := selectableContentRoots(tmp)
		if err != nil {
			return nil, err
		}
		entry.SelectableRoots = roots
		out.SelectableRoots = roots
		contentRoot := filepath.Join(tmp, filepath.FromSlash(rel))
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
	sort.Strings(out.FlatFileList)
	out.FlatFileList = out.FlatFileList[:min(len(out.FlatFileList), maxPreviewFlatFiles)]
	out.PreviewID = is.s.previews.put(entry)
	cached = true
	return out, nil
}

// extractArchive extracts absArchive into a fresh directory under the daemon's extraction root that the caller must remove.
func extractArchive(absArchive string) (string, error) {
	return extractArchiveWithBudget(absArchive, download.NewExtractBudget())
}

// extractArchiveWithBudget extracts absArchive into a fresh directory using the supplied operation budget.
func extractArchiveWithBudget(absArchive string, budget *download.ExtractBudget) (string, error) {
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
	if err := extractor.ExtractWithBudget(absArchive, tmp, budget); err != nil {
		os.RemoveAll(tmp)
		return "", fmt.Errorf("extracting: %w", err)
	}
	return tmp, nil
}

// extractionRoot returns, creating it, the private parent of this daemon instance's archive extractions under the temporary directory, keyed by the runtime directory whose lock admits one daemon, and refuses a path another user or a symlink holds.
func extractionRoot() (string, error) {
	sum := sha256.Sum256([]byte(config.RuntimeDir()))
	root := filepath.Join(os.TempDir(), fmt.Sprintf("gorganizer-extract-%d-%s", os.Getuid(), hex.EncodeToString(sum[:6])))
	if err := fsutil.EnsurePrivateDir(root); err != nil {
		return "", fmt.Errorf("extraction root: %w", err)
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
	if pe.GameID != req.GameID {
		return mismatch
	}
	if pe.ExternalArchivePath != "" {
		if req.ExternalArchivePath == "" {
			return mismatch
		}
		resolved, identity, err := resolveExternalArchive(req.ExternalArchivePath)
		if err != nil || resolved != pe.ExternalArchivePath || identity != pe.ExternalIdentity {
			return mismatch
		}
		return nil
	}
	if req.ArchiveRelPath == "" || req.ExternalArchivePath != "" {
		return mismatch
	}
	previewArchive, err := archivePath(config.DownloadsDir(pe.GameID), pe.ArchiveRelPath)
	if err != nil || previewArchive != absArchive {
		return mismatch
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

// installCtxErr returns a wrapped cancellation error before publication.
func installCtxErr(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("install stopped: %w", err)
	}
	return nil
}

// StartInstall installs an archive and publishes InstallCompleted once the install and its modlist registration succeeded.
func (is *InstallService) StartInstall(ctx context.Context, req dto.StartInstallRequest) (folder string, count int, err error) {
	if err = is.s.installOutcomes.register(req.GameID, req.ClientRequestID); err != nil {
		return "", 0, err
	}
	published := false
	defer func() {
		is.s.installOutcomes.finish(req.ClientRequestID, ctx, published, dto.InstallOutcome{ModFolder: folder, FileCount: count}, err)
	}()
	folder, count, err = is.startInstallFrom(ctx, req, "", &published)
	if err == nil {
		is.publishInstallCompleted(req.GameID, folder, req.ArchiveRelPath, nil, req.ClientRequestID)
	}
	return folder, count, err
}

// publishInstallCompleted announces a registered install on the game's install stream as a lossy refresh hint naming every dependency batch it satisfied.
func (is *InstallService) publishInstallCompleted(gameID, modName, archiveRelPath string, batchIDs []string, clientRequestID ...string) {
	completed := &dto.InstallCompletedResult{
		GameID: gameID, ModName: modName, ArchiveRelPath: archiveRelPath, BatchIDs: append([]string(nil), batchIDs...),
	}
	if len(clientRequestID) > 0 {
		completed.ClientRequestID = clientRequestID[0]
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
func (is *InstallService) startInstallFrom(ctx context.Context, req dto.StartInstallRequest, extracted string, published *bool) (string, int, error) {
	if err := is.s.awaitRecoveryCtx(ctx); err != nil {
		return "", 0, err
	}
	if err := is.s.refuseWhenShuttingDown(dto.BusyOperationInstall); err != nil {
		return "", 0, err
	}
	release, err := is.s.acquireShared(req.GameID, dto.BusyOperationInstall)
	if err != nil {
		return "", 0, err
	}
	defer release()
	if err := installCtxErr(ctx); err != nil {
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
	planner := layoutPlannerFor(req.GameID)
	if planner != nil && (req.FomodConfirmed || len(req.FomodSelectedFiles) > 0) {
		return "", 0, download.ErrFomodNotSupportedForLayout
	}
	if req.SelectedRoot != "" && (req.PreviewID == "" || planner != nil) {
		return "", 0, &UnsafePathError{Field: "selected_root"}
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
		if req.PreviewID == "" {
			if _, err := os.Stat(absArchive); err != nil {
				return "", 0, &ArchiveMissingError{GameID: req.GameID, Path: req.ArchiveRelPath}
			}
		}
		sidecar, _ = download.LoadSidecar(absArchive)
		indexRef.Path = filepath.Join("Downloads", req.ArchiveRelPath)
	} else {
		absArchive = req.ExternalArchivePath
		if req.PreviewID == "" {
			var err error
			absArchive, _, err = resolveExternalArchive(absArchive)
			if err != nil {
				return "", 0, err
			}
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
	if err := installCtxErr(ctx); err != nil {
		return "", 0, err
	}
	if err := checkModReplacement(config.ModsDir(req.GameID), target); err != nil {
		return "", 0, err
	}
	if req.Mode == dto.InstallMergeIntoMod || req.Mode == dto.InstallReplaceMod {
		if _, err := resolveExistingModDir(req.GameID, target); err != nil {
			var missing *ModNotFoundError
			if errors.As(err, &missing) {
				return "", 0, &download.InvalidTargetModError{Name: target, Reason: "merge target is not an existing mod folder"}
			}
			return "", 0, err
		}
	}
	if err := ensureModsDir(req.GameID); err != nil {
		return "", 0, err
	}
	if req.Mode == dto.InstallMergeIntoMod || req.Mode == dto.InstallReplaceMod {
		if err := download.ValidateMergeTarget(config.ModsDir(req.GameID), target); err != nil {
			return "", 0, err
		}
	}

	extractedRoot := extracted
	var contentRoot string
	var legacyFlatCopy bool
	var selectedFiles = req.FomodSelectedFiles
	if req.PreviewID != "" {
		pe := is.s.previews.acquire(req.PreviewID)
		if pe == nil {
			return "", 0, &PreviewNotFoundError{PreviewID: req.PreviewID}
		}
		defer is.s.previews.release(req.PreviewID)
		if err := previewMatchesRequest(pe, req, absArchive); err != nil {
			return "", 0, err
		}
		if req.ArchiveRelPath != "" {
			if _, err := os.Stat(absArchive); err != nil {
				return "", 0, &ArchiveMissingError{GameID: req.GameID, Path: req.ArchiveRelPath}
			}
		}
		if pe.ExternalArchivePath != "" {
			absArchive = pe.ExternalArchivePath
			indexRef.Path = absArchive
		}
		extractedRoot = pe.ExtractRoot
		if req.SelectedRoot != "" {
			if pe.HasFomod || !slices.Contains(pe.SelectableRoots, req.SelectedRoot) {
				return "", 0, &UnsafePathError{Field: "selected_root"}
			}
			contentRoot = filepath.Join(pe.ExtractRoot, filepath.FromSlash(req.SelectedRoot))
		} else if !pe.HasFomod && planner == nil {
			contentRoot = filepath.Join(pe.ExtractRoot, filepath.FromSlash(pe.DetectedRoot))
		}
		if pe.HasFomod && req.FomodConfirmed {
			extractedRoot = pe.ModuleRoot
			if pe.LegacyInfoOnly {
				legacyFlatCopy = true
				contentRoot = pe.ModuleRoot
				selectedFiles = nil
			} else if len(selectedFiles) == 0 {
				selectedFiles = pe.RequiredFiles
				if len(selectedFiles) == 0 {
					return "", 0, download.ErrEmptyInstallSelection
				}
			}
		} else if pe.HasFomod && len(selectedFiles) > 0 {
			extractedRoot = pe.ModuleRoot
		} else if !pe.HasFomod && len(selectedFiles) > 0 {
			return "", 0, &UnsafePathError{Field: "fomod_selected_files"}
		}
	}

	var stagedComplete *download.InstallProgress
	publishProgress := func(p download.InstallProgress) {
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
	sink := func(p download.InstallProgress) {
		if is.s.installBeforePublish != nil && p.Step == download.StageFinalizing {
			is.s.installBeforePublish()
		}
		if is.s.installCopyProgress != nil && p.Step == download.StageCopying {
			is.s.installCopyProgress(p)
		}
		if (req.Mode == dto.InstallMergeIntoMod || req.Mode == dto.InstallReplaceMod) && p.Step == download.StageComplete {
			stagedComplete = &p
			return
		}
		publishProgress(p)
	}

	var fomodFiles []download.FomodFile
	for _, f := range selectedFiles {
		fomodFiles = append(fomodFiles, download.FomodFile{
			Source: f.Source, Destination: f.Destination,
			IsFolder: f.IsFolder, Priority: f.Priority,
		})
	}

	installReq := download.InstallRequest{
		Context: ctx,
		OnPublished: func() {
			*published = true
			if is.s.installAfterPublish != nil {
				is.s.installAfterPublish()
			}
		},
		GameID:              req.GameID,
		ArchivePath:         absArchive,
		ExtractedRoot:       extractedRoot,
		ContentRoot:         contentRoot,
		LegacyFomodFlatCopy: legacyFlatCopy,
		Mode:                download.InstallMode(req.Mode),
		TargetMod:           target,
		SourceArchiveRef:    indexRef,
		FomodSelectedFiles:  fomodFiles,
		ProgressSink:        sink,
		Layout:              planner,
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

	var stageToken, stageDir string
	var mergeSnapshot *download.ModMetadata
	if req.Mode == dto.InstallMergeIntoMod || req.Mode == dto.InstallReplaceMod {
		if req.Mode == dto.InstallMergeIntoMod {
			var err error
			mergeSnapshot, err = download.LoadModMetadata(filepath.Join(config.ModsDir(req.GameID), target))
			if err != nil {
				return "", 0, fmt.Errorf("reading mod metadata: %w", err)
			}
		}
		stageToken = uuid.NewString()
		stageDir = filepath.Join(config.ModsDir(req.GameID), reinstallStagePrefix+stageToken)
		if req.Mode == dto.InstallMergeIntoMod {
			if err := prepareMergeStage(filepath.Join(config.ModsDir(req.GameID), target), stageDir); err != nil {
				return "", 0, err
			}
		} else {
			if err := os.Mkdir(stageDir, 0755); err != nil {
				return "", 0, fmt.Errorf("creating replace stage: %w", err)
			}
			installReq.DeferIndexUpdate = true
		}
		installReq.Mode = download.ModeMergeIntoMod
		installReq.TargetMod = reinstallStagePrefix + stageToken
		installReq.RecordModName = target
	}
	result, err := download.Install(installReq)
	if err != nil {
		if stageDir != "" {
			_ = os.RemoveAll(stageDir)
		}
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
	if stageDir != "" {
		if err := installCtxErr(ctx); err != nil {
			_ = os.RemoveAll(stageDir)
			return "", 0, err
		}
		*published = true
		var publishErr error
		if req.Mode == dto.InstallReplaceMod {
			publishErr = is.publishPreparedReplace(req.GameID, target, stageToken)
		} else {
			publishErr = is.publishPreparedMerge(req.GameID, target, stageToken, mergeSnapshot)
		}
		if publishErr != nil {
			sink(download.InstallProgress{InstallID: result.InstallID, Step: download.StageFailed, Error: publishErr.Error()})
			return "", 0, publishErr
		}
		if is.s.installAfterPublish != nil {
			is.s.installAfterPublish()
		}
		if req.Mode == dto.InstallReplaceMod && req.ArchiveRelPath != "" {
			if err := download.SetUninstalled(req.GameID, req.ArchiveRelPath, false); err != nil {
				slog.Warn("updating download index failed", "err", err)
			}
		}
		if stagedComplete != nil {
			publishProgress(*stagedComplete)
		}
		result.ModFolder = target
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
