package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/ipc"
	"github.com/parka/gorganizer/internal/mod"
)

// previewEntry is one daemon-side cached archive extraction keyed by
// preview_id. The lifecycle is (PreviewInstall → optional StartInstall →
// DiscardPreview or TTL eviction). Values are small: an extract path plus
// a couple of strings; the heavy on-disk cost is the extracted tree,
// cleaned up on eviction.
type previewEntry struct {
	GameID         string
	ArchiveRelPath string
	ExtractRoot    string
	CreatedAt      time.Time
	HasFomod       bool
	ModuleRoot     string // fomod/ParentDir when HasFomod, else ""
}

// previewCache is the daemon-scoped cache for FOMOD-aware install previews.
// TTL eviction runs on a background goroutine; the bound keeps disk use
// capped even if the user abandons a dialog without calling DiscardPreview.
type previewCache struct {
	mu      sync.Mutex
	entries map[string]*previewEntry
	ttl     time.Duration
	maxLen  int
}

func newPreviewCache(ttl time.Duration, maxLen int) *previewCache {
	return &previewCache{
		entries: make(map[string]*previewEntry),
		ttl:     ttl,
		maxLen:  maxLen,
	}
}

func (c *previewCache) put(entry *previewEntry) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Evict oldest if over the cap.
	for len(c.entries) >= c.maxLen {
		var oldestID string
		var oldestT time.Time
		for id, e := range c.entries {
			if oldestID == "" || e.CreatedAt.Before(oldestT) {
				oldestID, oldestT = id, e.CreatedAt
			}
		}
		if oldestID == "" {
			break
		}
		old := c.entries[oldestID]
		delete(c.entries, oldestID)
		os.RemoveAll(old.ExtractRoot)
	}
	id := "prev-" + uuid.NewString()
	entry.CreatedAt = time.Now()
	c.entries[id] = entry
	return id
}

func (c *previewCache) get(id string) *previewEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[id]
	if !ok {
		return nil
	}
	// Refresh the TTL on read — a user walking a FOMOD wizard shouldn't
	// have their extraction GC'd mid-flow.
	e.CreatedAt = time.Now()
	return e
}

func (c *previewCache) discard(id string) bool {
	c.mu.Lock()
	e, ok := c.entries[id]
	if ok {
		delete(c.entries, id)
	}
	c.mu.Unlock()
	if ok && e != nil {
		os.RemoveAll(e.ExtractRoot)
		return true
	}
	return false
}

// sweep evicts expired entries. Called periodically by runPreviewSweeper.
func (c *previewCache) sweep() {
	c.mu.Lock()
	now := time.Now()
	var expired []*previewEntry
	for id, e := range c.entries {
		if now.Sub(e.CreatedAt) > c.ttl {
			expired = append(expired, e)
			delete(c.entries, id)
		}
	}
	c.mu.Unlock()
	for _, e := range expired {
		os.RemoveAll(e.ExtractRoot)
	}
}

func (d *Daemon) runPreviewSweeper() {
	t := time.NewTicker(2 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-d.shutdownCh:
			return
		case <-t.C:
			d.previews.sweep()
		}
	}
}

// --- ipc.ArchiveController ---

// StartDownload enqueues a new download from an NXM URI. Wraps the download
// manager so daemon-level policy (require initialized manager, surface
// structured errors) lives here.
func (d *Daemon) StartDownload(nxmURI string) (string, int, error) {
	if d.downloadMgr == nil {
		// Also surface to the global status stream so the running MainWindow
		// shows a banner when an NXM forwarded by `gorganizerd --handle-nxm`
		// lands here. Without this the failure goes only to the forwarder's
		// stderr (a detached child of the browser) and the user sees nothing.
		const msg = "NXM ignored: no Nexus API key set — open Settings to add one"
		select {
		case d.statusCh <- ipc.StatusEventResult{Error: msg}:
		default:
		}
		return "", 0, fmt.Errorf("download manager not initialized (set nexus_api_key in config)")
	}
	return d.downloadMgr.StartDownload(nxmURI)
}

func (d *Daemon) CancelDownload(id string) error {
	if d.downloadMgr == nil {
		return &ipc.DownloadNotFoundError{ID: id}
	}
	return d.downloadMgr.CancelDownload(id)
}

func (d *Daemon) RetryDownload(id string) (int, error) {
	if d.downloadMgr == nil {
		return 0, fmt.Errorf("download manager not initialized")
	}
	return d.downloadMgr.RetryDownload(id)
}

// ListArchives returns the per-game Downloads view — every archive in the
// index, enriched from its sidecar, with status derived from installed
// mods' source_archives + the live download manager's queue.
func (d *Daemon) ListArchives(gameID string) ([]ipc.ArchiveRowResult, error) {
	if _, ok := d.config.Games[gameID]; !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	idx, err := download.LoadIndex(gameID)
	if err != nil {
		return nil, err
	}
	installedBy := d.installedArchiveMap(gameID)
	downloadsDir := config.DownloadsDir(gameID)

	rows := make([]ipc.ArchiveRowResult, 0, len(idx.Archives))
	for _, e := range idx.Archives {
		absArchive := filepath.Join(downloadsDir, e.Path)
		row := ipc.ArchiveRowResult{
			ArchiveRelPath:  e.Path,
			ModID:           e.ModID,
			FileID:          e.FileID,
			FileArchiveName: filepath.Base(e.Path),
			Hidden:          e.Hidden,
		}
		if sc, err := download.LoadSidecar(absArchive); err == nil {
			row.ModName = sc.ModName
			row.FileName = sc.FileName
			row.FileArchiveName = sc.FileArchiveName
			row.Version = sc.Version
			row.Category = sc.Category
			row.SizeBytes = sc.SizeBytes
			row.UploadedAt = sc.UploadedAt
			row.DownloadedAt = sc.DownloadedAt
			row.GameDomain = sc.GameDomain
			row.ThumbnailURL = sc.ThumbnailURL
			row.AdultContent = sc.AdultContent
		}
		lookupKey := filepath.Join("Downloads", e.Path)
		fileExists := false
		if fi, err := os.Stat(absArchive); err == nil && !fi.IsDir() {
			fileExists = true
		}
		installRec := installedBy[lookupKey]
		switch {
		case installRec.Folder != "":
			row.Status = ipc.DownloadStatusInstalled
			row.InstalledModFolder = installRec.Folder
			row.Merged = installRec.Merged
		case !fileExists && d.downloadMgr != nil:
			row.Status = ipc.DownloadStatusDownloading
			row.DownloadID = d.downloadMgr.ActiveDownloadIDByArchive(absArchive)
		case fileExists && e.Uninstalled:
			row.Status = ipc.DownloadStatusUninstalled
		case fileExists:
			row.Status = ipc.DownloadStatusDownloaded
		default:
			row.Status = ipc.DownloadStatusUnknown
		}
		if row.DownloadID == "" && d.downloadMgr != nil {
			row.DownloadID = d.downloadMgr.ActiveDownloadIDByArchive(absArchive)
		}
		rows = append(rows, row)
	}

	// Append phantom rows for ledger-tracked downloads that haven't landed
	// on disk yet (queued, in-flight, resumed-after-restart). Frontend
	// promotes these in place once the real archive appears.
	//
	// Two filters keep us from duplicating real archives:
	//   1. Skip terminal entries (Downloaded / Cancelled / Failed) — the
	//      downloader removes these on success but stale entries can
	//      survive a hard kill (which my smoke test just demonstrated).
	//   2. Skip entries whose ArchiveRelPath also appears in the index —
	//      the index entry already represents this archive with full
	//      sidecar metadata; the ledger row would be a stripped-down
	//      duplicate showing only "Downloaded" + size.
	indexed := make(map[string]struct{}, len(idx.Archives))
	for _, e := range idx.Archives {
		indexed[e.Path] = struct{}{}
	}
	if entries, err := download.LoadLedger(gameID); err == nil {
		var toEvict []string
		for _, le := range entries {
			if le.Terminal() || le.Status == "" {
				toEvict = append(toEvict, le.ID)
				continue
			}
			if _, dup := indexed[le.ArchiveRelPath]; dup {
				// Index entry already covers this archive — the ledger row
				// is leftover bookkeeping. Evict so future calls are clean.
				toEvict = append(toEvict, le.ID)
				continue
			}
			rows = append(rows, ipc.ArchiveRowResult{
				ArchiveRelPath:  le.ArchiveRelPath,
				DownloadID:      le.ID,
				ModID:           le.ModID,
				FileID:          le.FileID,
				GameDomain:      le.GameSlug,
				BytesDownloaded: le.BytesDone,
				SizeBytes:       le.BytesTotal,
				Status:          ledgerToDownloadStatus(le.Status),
			})
		}
		for _, id := range toEvict {
			_ = download.RemoveLedgerEntry(gameID, id)
		}
	}
	return rows, nil
}

func ledgerToDownloadStatus(ls download.LedgerStatus) ipc.DownloadStatus {
	switch ls {
	case download.LedgerQueued:
		return ipc.DownloadStatusQueued
	case download.LedgerDownloading:
		return ipc.DownloadStatusDownloading
	case download.LedgerDownloaded:
		return ipc.DownloadStatusDownloaded
	case download.LedgerCancelled:
		return ipc.DownloadStatusCancelled
	case download.LedgerFailed:
		return ipc.DownloadStatusFailed
	}
	return ipc.DownloadStatusUnknown
}

// RemoveArchive deletes an archive, its sidecar, and the index entry.
// Replaces the v1 DeleteDownload.
func (d *Daemon) RemoveArchive(gameID, archiveRelPath string) error {
	if _, ok := d.config.Games[gameID]; !ok {
		return fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	downloadsDir := config.DownloadsDir(gameID)
	absArchive := filepath.Join(downloadsDir, archiveRelPath)
	_ = os.Remove(absArchive)
	_ = os.Remove(download.SidecarPath(absArchive))
	// Also remove any lingering .part file.
	_ = os.Remove(download.PartPath(absArchive))
	if err := download.RemoveEntry(gameID, archiveRelPath); err != nil {
		return err
	}
	d.invalidateInstalledArchiveCache(gameID)
	d.archiveBus.Publish(gameID, ipc.ArchiveEventResult{
		GameID: gameID, ArchiveRemoved: archiveRelPath,
	})
	return nil
}

func (d *Daemon) SetArchiveHidden(gameID, archiveRelPath string, hidden bool) error {
	if _, ok := d.config.Games[gameID]; !ok {
		return fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	if err := download.SetHidden(gameID, archiveRelPath, hidden); err != nil {
		return err
	}
	if row, err := d.buildArchiveRow(gameID, archiveRelPath); err == nil {
		d.archiveBus.Publish(gameID, ipc.ArchiveEventResult{GameID: gameID, RowChanged: row})
	}
	return nil
}

func (d *Daemon) SetArchivesHiddenBulk(gameID string, hidden bool, scope ipc.BulkHideScope) (int, error) {
	if _, ok := d.config.Games[gameID]; !ok {
		return 0, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	installedBy := d.installedArchiveMap(gameID)
	pred := func(e download.IndexEntry) bool {
		_, isInstalled := installedBy[filepath.Join("Downloads", e.Path)]
		switch scope {
		case ipc.BulkHideAll:
			return true
		case ipc.BulkHideInstalled:
			return isInstalled
		case ipc.BulkHideUninstalled:
			return !isInstalled
		}
		return false
	}
	idx, err := download.LoadIndex(gameID)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, e := range idx.Archives {
		if pred(e) && e.Hidden != hidden {
			count++
		}
	}
	if err := download.SetHiddenBulk(gameID, hidden, pred); err != nil {
		return 0, err
	}
	return count, nil
}

// RefreshArchiveMetadata re-fetches the archive's sidecar from Nexus and
// rewrites it. Useful when the mod version bumps or category changes on
// Nexus and the user wants the local metadata to reflect the newer state.
func (d *Daemon) RefreshArchiveMetadata(gameID, archiveRelPath string) (*ipc.ArchiveRowResult, error) {
	gc, ok := d.config.Games[gameID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	_ = gc
	if d.config.NexusAPIKey == "" {
		return nil, fmt.Errorf("nexus API key required — paste one in Tools \u2192 Settings")
	}
	downloadsDir := config.DownloadsDir(gameID)
	absArchive := filepath.Join(downloadsDir, archiveRelPath)
	if _, err := os.Stat(absArchive); err != nil {
		return nil, &ipc.ArchiveMissingError{GameID: gameID, Path: archiveRelPath}
	}
	sc, err := download.LoadSidecar(absArchive)
	if err != nil || sc == nil || sc.ModID == 0 || sc.GameDomain == "" {
		return nil, fmt.Errorf("sidecar missing the Nexus ids needed to refresh — cannot refresh")
	}
	nx := download.NewNexusClient(d.config.NexusAPIKey)
	info, err := nx.GetModInfo(sc.GameDomain, sc.ModID)
	if err != nil {
		return nil, fmt.Errorf("fetching mod info: %w", err)
	}
	details, err := nx.GetFileDetails(sc.GameDomain, sc.ModID, sc.FileID)
	if err != nil {
		return nil, fmt.Errorf("fetching file details: %w", err)
	}
	// Preserve what the old sidecar knew; update what Nexus tells us.
	updated := *sc
	if info != nil {
		updated.ModName = info.Name
		updated.ThumbnailURL = info.PictureURL
		updated.AdultContent = info.ContainsAdult
	}
	if details != nil {
		updated.FileName = details.Name
		updated.Version = details.Version
		updated.Category = download.NormalizeCategory(details.CategoryName)
		if details.UploadedTime != "" {
			updated.UploadedAt = details.UploadedTime
		}
	}
	if err := download.SaveSidecar(absArchive, updated, time.Now()); err != nil {
		return nil, fmt.Errorf("writing sidecar: %w", err)
	}
	row, err := d.buildArchiveRow(gameID, archiveRelPath)
	if err != nil {
		return nil, err
	}
	d.archiveBus.Publish(gameID, ipc.ArchiveEventResult{GameID: gameID, RowChanged: row})
	return row, nil
}

// buildArchiveRow composes a single archive row on demand. Shared between
// SetArchiveHidden, RefreshArchiveMetadata, and row-changed event emission.
func (d *Daemon) buildArchiveRow(gameID, archiveRelPath string) (*ipc.ArchiveRowResult, error) {
	downloadsDir := config.DownloadsDir(gameID)
	absArchive := filepath.Join(downloadsDir, archiveRelPath)

	idx, err := download.LoadIndex(gameID)
	if err != nil {
		return nil, err
	}
	var idxEntry *download.IndexEntry
	for i := range idx.Archives {
		if idx.Archives[i].Path == archiveRelPath {
			idxEntry = &idx.Archives[i]
			break
		}
	}
	if idxEntry == nil {
		return nil, &ipc.ArchiveMissingError{GameID: gameID, Path: archiveRelPath}
	}

	row := ipc.ArchiveRowResult{
		ArchiveRelPath:  archiveRelPath,
		ModID:           idxEntry.ModID,
		FileID:          idxEntry.FileID,
		FileArchiveName: filepath.Base(archiveRelPath),
		Hidden:          idxEntry.Hidden,
	}
	if sc, err := download.LoadSidecar(absArchive); err == nil {
		row.ModName = sc.ModName
		row.FileName = sc.FileName
		row.FileArchiveName = sc.FileArchiveName
		row.Version = sc.Version
		row.Category = sc.Category
		row.SizeBytes = sc.SizeBytes
		row.UploadedAt = sc.UploadedAt
		row.DownloadedAt = sc.DownloadedAt
		row.GameDomain = sc.GameDomain
		row.ThumbnailURL = sc.ThumbnailURL
		row.AdultContent = sc.AdultContent
	}
	installedBy := d.installedArchiveMap(gameID)
	fileExists := false
	if fi, err := os.Stat(absArchive); err == nil && !fi.IsDir() {
		fileExists = true
	}
	lookupKey := filepath.Join("Downloads", archiveRelPath)
	installRec := installedBy[lookupKey]
	switch {
	case installRec.Folder != "":
		row.Status = ipc.DownloadStatusInstalled
		row.InstalledModFolder = installRec.Folder
		row.Merged = installRec.Merged
	case fileExists && idxEntry.Uninstalled:
		row.Status = ipc.DownloadStatusUninstalled
	case fileExists:
		row.Status = ipc.DownloadStatusDownloaded
	default:
		row.Status = ipc.DownloadStatusUnknown
	}
	return &row, nil
}

// StreamArchiveEvents subscribes the caller to per-game archive stream
// events. The returned channel closes when the caller's ctx is cancelled.
func (d *Daemon) StreamArchiveEvents(ctx context.Context, gameID string) (<-chan ipc.ArchiveEventResult, error) {
	if _, ok := d.config.Games[gameID]; !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	ch, _ := d.archiveBus.Subscribe(ctx, gameID)
	return ch, nil
}

// StreamInstallEvents subscribes the caller to per-game install progress.
func (d *Daemon) StreamInstallEvents(ctx context.Context, gameID string) (<-chan ipc.InstallEventResult, error) {
	if _, ok := d.config.Games[gameID]; !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	ch, _ := d.installBus.Subscribe(ctx, gameID)
	return ch, nil
}

// --- ipc.InstallController ---

// PreviewInstall extracts an archive into a daemon-cached tmpdir and
// returns either a FOMOD plan for the UI wizard or a flat file listing.
// The caller either follows up with StartInstall (which reuses the cache)
// or DiscardPreview (which drops it).
func (d *Daemon) PreviewInstall(gameID, archiveRelPath string) (*ipc.PreviewResult, error) {
	if _, ok := d.config.Games[gameID]; !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	downloadsDir := config.DownloadsDir(gameID)
	absArchive := filepath.Join(downloadsDir, archiveRelPath)
	if _, err := os.Stat(absArchive); err != nil {
		return nil, &ipc.ArchiveMissingError{GameID: gameID, Path: archiveRelPath}
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
	// Legacy NMM-style installers nest a *.fomod (itself an archive) inside
	// the outer download. Without this expansion FindFomodRoot would never
	// see a fomod/ tree for those archives.
	download.ExpandNestedFomods(tmp)
	entry := &previewEntry{
		GameID: gameID, ArchiveRelPath: archiveRelPath, ExtractRoot: tmp,
	}
	out := &ipc.PreviewResult{}
	if root, kind := download.FindFomodRootKind(tmp); kind != download.FomodKindNone {
		entry.HasFomod = true
		entry.ModuleRoot = root
		out.HasFomod = true
		switch kind {
		case download.FomodKindModuleConfig:
			// Modern XML wizard. The frontend parses ModuleConfig.xml itself
			// from ModulePath; we just pass the root through.
			out.Plan = &ipc.FomodPlanResult{
				ModuleName: filepath.Base(archiveRelPath),
				ModulePath: root,
			}
		case download.FomodKindLegacyInfoOnly:
			info := download.ParseLegacyFomodInfo(root)
			out.Plan = &ipc.FomodPlanResult{
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
		// Flat list, relative paths under the content root.
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
	out.PreviewID = d.previews.put(entry)
	return out, nil
}

// DiscardPreview drops a cached preview's extraction.
func (d *Daemon) DiscardPreview(previewID string) error {
	if !d.previews.discard(previewID) {
		return &ipc.PreviewNotFoundError{PreviewID: previewID}
	}
	return nil
}

// StartInstall is the unified install entry point. archive_rel_path and
// external_archive_path are mutually exclusive — StartInstall fails if
// both or neither is set. Every successful install writes source_archives
// (no more "drag-drop can't reinstall" blind spot).
func (d *Daemon) StartInstall(req ipc.StartInstallRequest) (string, int, error) {
	if _, ok := d.config.Games[req.GameID]; !ok {
		return "", 0, fmt.Errorf("%w: %s", config.ErrInvalidGameID, req.GameID)
	}
	if (req.ArchiveRelPath == "") == (req.ExternalArchivePath == "") {
		return "", 0, fmt.Errorf("exactly one of archive_rel_path or external_archive_path must be set")
	}

	// Resolve on-disk archive.
	var absArchive string
	var sidecar *download.ArchiveSidecar
	var indexRef download.SourceArchiveRef
	if req.ArchiveRelPath != "" {
		downloadsDir := config.DownloadsDir(req.GameID)
		absArchive = filepath.Join(downloadsDir, req.ArchiveRelPath)
		if _, err := os.Stat(absArchive); err != nil {
			return "", 0, &ipc.ArchiveMissingError{GameID: req.GameID, Path: req.ArchiveRelPath}
		}
		sidecar, _ = download.LoadSidecar(absArchive)
		indexRef.Path = filepath.Join("Downloads", req.ArchiveRelPath)
	} else {
		absArchive = req.ExternalArchivePath
		if _, err := os.Stat(absArchive); err != nil {
			return "", 0, fmt.Errorf("external archive not found: %w", err)
		}
		// Use the archive's on-disk path as the source_archive entry. If
		// the user wants it tracked in the Downloads index, they can import
		// separately.
		indexRef.Path = absArchive
	}
	if sidecar != nil {
		indexRef.ModID = sidecar.ModID
		indexRef.FileID = sidecar.FileID
	}

	// Resolve target mod folder.
	target := req.TargetMod
	if req.Mode == ipc.InstallAsNewMod {
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

	// Resolve any preview cache hit.
	var extractedRoot string
	if req.PreviewID != "" {
		pe := d.previews.get(req.PreviewID)
		if pe == nil {
			return "", 0, &ipc.PreviewNotFoundError{PreviewID: req.PreviewID}
		}
		extractedRoot = pe.ExtractRoot
	}

	// Emit progress via the per-game install bus.
	sink := func(p download.InstallProgress) {
		d.installBus.Publish(req.GameID, ipc.InstallEventResult{
			GameID: req.GameID,
			Progress: &ipc.InstallProgressResult{
				InstallID:      p.InstallID,
				ArchiveRelPath: req.ArchiveRelPath,
				ModName:        target,
				Step:           ipc.InstallStep(p.Step),
				Pct:            p.Pct,
				CurrentFile:    p.CurrentFile,
				FilesDone:      p.FilesDone,
				FilesTotal:     p.FilesTotal,
				Error:          p.Error,
				GameID:         req.GameID,
			},
		})
	}

	// Marshal FOMOD selections from IPC → download types.
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
		// Translate install markers into IPC error types so the daemon
		// surfaces actionable failures instead of generic strings.
		if path, ok := download.IsFomodMarker(err); ok {
			return "", 0, &ipc.FomodRequiredError{
				GameID: req.GameID, Path: path, PreviewID: req.PreviewID,
			}
		}
		if name, ok := download.IsCollisionMarker(err); ok {
			return "", 0, &ipc.ModCollisionError{Name: name}
		}
		return "", 0, err
	}

	// Preview's job is done; drop it from the cache.
	if req.PreviewID != "" {
		d.previews.discard(req.PreviewID)
	}

	// Bookkeeping that the daemon owns (download package deliberately
	// doesn't touch these).
	d.invalidateInstalledArchiveCache(req.GameID)
	if req.Mode == ipc.InstallAsNewMod {
		d.ensureInModList(req.GameID, result.ModFolder)
	}

	// Emit a RowChanged event so the Downloads tab flips to INSTALLED
	// without a full ListArchives reload.
	if req.ArchiveRelPath != "" {
		if row, err := d.buildArchiveRow(req.GameID, req.ArchiveRelPath); err == nil {
			d.archiveBus.Publish(req.GameID, ipc.ArchiveEventResult{
				GameID: req.GameID, RowChanged: row,
			})
		}
	}

	slog.Info("install complete",
		"game", req.GameID, "mod", result.ModFolder, "files", result.FileCount)
	return result.ModFolder, result.FileCount, nil
}

// --- ipc.ModController — v2 additions ---

// ListMods enumerates installed mods for a game.
func (d *Daemon) ListMods(gameID string) ([]ipc.ModInfoResult, error) {
	modsDir := config.ModsDir(gameID)
	mods, err := mod.ListMods(modsDir, gameID)
	if err != nil {
		return nil, err
	}
	var results []ipc.ModInfoResult
	for _, m := range mods {
		results = append(results, ipc.ModInfoResult{
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
func (d *Daemon) GetMod(gameID, modName string) (*ipc.ModInfoResult, error) {
	modDir := filepath.Join(config.ModsDir(gameID), modName)
	if _, err := os.Stat(modDir); err != nil {
		if os.IsNotExist(err) {
			return nil, &ipc.ModNotFoundError{GameID: gameID, Name: modName}
		}
		return nil, err
	}
	m := mod.NewMod(modName, gameID, modDir)
	return &ipc.ModInfoResult{
		Name:     m.Name,
		GameID:   m.GameID,
		BasePath: m.BasePath,
	}, nil
}

// RescanMod rewalks a mod folder and returns the full file list.
func (d *Daemon) RescanMod(gameID, modName string) (*ipc.ModInfoResult, error) {
	modDir := filepath.Join(config.ModsDir(gameID), modName)
	if _, err := os.Stat(modDir); err != nil {
		if os.IsNotExist(err) {
			return nil, &ipc.ModNotFoundError{GameID: gameID, Name: modName}
		}
		return nil, err
	}
	m := mod.NewMod(modName, gameID, modDir)
	if err := m.Scan(); err != nil {
		return nil, err
	}
	return &ipc.ModInfoResult{
		Name:      m.Name,
		GameID:    m.GameID,
		BasePath:  m.BasePath,
		FileCount: m.FileCount,
		TotalSize: m.TotalSize,
		Files:     m.Files,
	}, nil
}

// RenameMod atomically renames a mod folder and updates every profile's
// modlist.txt. VFS is rebuilt in place when the game is mounted.
func (d *Daemon) RenameMod(gameID, oldName, newName string) error {
	if _, ok := d.config.Games[gameID]; !ok {
		return fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	if oldName == newName {
		return nil
	}
	modsDir := config.ModsDir(gameID)
	src := filepath.Join(modsDir, oldName)
	dst := filepath.Join(modsDir, newName)
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return &ipc.ModNotFoundError{GameID: gameID, Name: oldName}
		}
		return err
	}
	if _, err := os.Stat(dst); err == nil {
		return &ipc.ModCollisionError{Name: newName}
	}
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("renaming mod folder: %w", err)
	}

	// Update folder field in metadata.yaml. Best-effort: if this fails we
	// roll the directory rename back so state stays consistent.
	meta, _ := download.LoadModMetadata(dst)
	if meta != nil {
		meta.Folder = newName
		if meta.Name == oldName {
			meta.Name = newName
		}
		_ = download.SaveModMetadata(dst, meta)
	}

	// Rewrite every profile's modlist.txt.
	profiles, _ := d.profileMgr.List(gameID)
	for _, p := range profiles {
		_, entries, err := d.profileMgr.Load(gameID, p.Name)
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
			_ = d.profileMgr.Save(p, entries)
		}
	}

	d.invalidateInstalledArchiveCache(gameID)
	// Rebuild VFS if the game is mounted — mod folder name is in the
	// layer list.
	d.mu.RLock()
	mm, mmOk := d.mountMgrs[gameID]
	ms, msOk := d.mountStates[gameID]
	gc, gcOk := d.config.Games[gameID]
	d.mu.RUnlock()
	if mmOk && msOk && gcOk && mm.IsMounted() {
		if _, entries, err := d.profileMgr.Load(gameID, ms.profileName); err == nil {
			layers := d.buildLayers(gameID, gc, entries)
			_ = mm.Tree().Rebuild(layers)
		}
	}
	return nil
}

// UninstallMod removes a mod's install dir, strips it from every profile's
// modlist, and flips the sticky Uninstalled bit on archives owned solely by
// this mod. Returns the list of archive paths flagged so the frontend can
// refresh those rows without a full reload.
//
// When `force=false` and the mod is enabled in any profile, returns a
// ModInUseError listing the profiles. The frontend presents a confirm
// dialog and re-issues with force=true.
func (d *Daemon) UninstallMod(gameID, modName string, force bool) ([]string, error) {
	if _, ok := d.config.Games[gameID]; !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	modDir := filepath.Join(config.ModsDir(gameID), modName)
	meta, err := download.LoadModMetadata(modDir)
	if err != nil || meta == nil || (len(meta.SourceArchives) == 0 && meta.Name == "") {
		// Mod folder absent is legitimate — nothing to do.
		if _, statErr := os.Stat(modDir); os.IsNotExist(statErr) {
			return nil, &ipc.ModNotFoundError{GameID: gameID, Name: modName}
		}
		if err != nil {
			return nil, fmt.Errorf("reading mod metadata: %w", err)
		}
	}

	// Check which profiles have this mod enabled.
	profiles, _ := d.profileMgr.List(gameID)
	var enabledIn []string
	for _, p := range profiles {
		_, entries, err := d.profileMgr.Load(gameID, p.Name)
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
		return nil, &ipc.ModInUseError{Name: modName, Profiles: enabledIn}
	}

	// Flip sticky Uninstalled on archives used SOLELY by this mod.
	ownedSolely := map[string]bool{}
	if meta != nil {
		for _, sa := range meta.SourceArchives {
			ownedSolely[sa.Path] = true
		}
	}
	if len(ownedSolely) > 0 {
		// Scan every other mod — if any one references an archive, un-flag it.
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

	// Strip from every profile's modlist.txt.
	for _, p := range profiles {
		_, entries, err := d.profileMgr.Load(gameID, p.Name)
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
			_ = d.profileMgr.Save(p, kept)
		}
	}

	// Remove mod folder. Retry once on EBUSY — FUSE may be holding a file
	// open briefly during a concurrent read.
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

	// Flip sticky-uninstalled. Input paths look like "Downloads/<rel>"; the
	// index is keyed by <rel>, so strip the prefix.
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
		// Broadcast a RowChanged so the Downloads tab flips without a reload.
		if row, err := d.buildArchiveRow(gameID, rel); err == nil {
			d.archiveBus.Publish(gameID, ipc.ArchiveEventResult{
				GameID: gameID, RowChanged: row,
			})
		}
	}

	d.invalidateInstalledArchiveCache(gameID)

	// Rebuild VFS if mounted.
	d.mu.RLock()
	mm, mmOk := d.mountMgrs[gameID]
	ms, msOk := d.mountStates[gameID]
	gc, gcOk := d.config.Games[gameID]
	d.mu.RUnlock()
	if mmOk && msOk && gcOk && mm.IsMounted() {
		if _, entries, err := d.profileMgr.Load(gameID, ms.profileName); err == nil {
			layers := d.buildLayers(gameID, gc, entries)
			_ = mm.Tree().Rebuild(layers)
		}
	}
	slog.Info("mod uninstalled", "game", gameID, "mod", modName, "archives_flagged", flagged)
	return flagged, nil
}

// ReinstallMod clears the mod's file tree and replays every archive in its
// source_archives via the canonical Install path. Missing archives are
// skipped; the rest still run.
func (d *Daemon) ReinstallMod(gameID, modName string) (int, int, int, error) {
	if _, ok := d.config.Games[gameID]; !ok {
		return 0, 0, 0, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
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
	// Preserve identity fields; wipe accumulations so each replay re-builds.
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
		// Use MergeInto so each replay overlays onto the same folder.
		sink := func(p download.InstallProgress) {
			d.installBus.Publish(gameID, ipc.InstallEventResult{
				GameID: gameID,
				Progress: &ipc.InstallProgressResult{
					InstallID: p.InstallID, ModName: modName,
					Step: ipc.InstallStep(p.Step), Pct: p.Pct,
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
	d.invalidateInstalledArchiveCache(gameID)
	return replayed, skipped, fileCount, nil
}
