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

type previewEntry struct {
	GameID         string
	ArchiveRelPath string
	ExtractRoot    string
	CreatedAt      time.Time
	HasFomod       bool
	ModuleRoot     string

	// leases counts in-flight readers of ExtractRoot (an install using this
	// preview). While leased, eviction is deferred by setting pendingEvict; the
	// last release performs the RemoveAll (Guard R3, H-12).
	leases       int
	pendingEvict bool
}

// previewCache is the daemon-scoped cache for FOMOD-aware install previews.
// TTL eviction runs on a background goroutine; the bound keeps disk use
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
	// Evict oldest UNLEASED entries toward the bound. A leased entry is skipped
	// (it is reclaimed on release), so the cache may briefly exceed maxLen rather
	// than spin or rm an extract dir an install is still reading (Guard R3).
	for len(c.entries) >= c.maxLen && c.evictOldestUnleasedLocked() {
	}
	id := "prev-" + uuid.NewString()
	entry.CreatedAt = time.Now()
	c.entries[id] = entry
	return id
}

// evictOldestUnleasedLocked removes the oldest entry that has no active lease
// and is not already pending eviction, returning true if one was evicted.
// Caller holds c.mu.
func (c *previewCache) evictOldestUnleasedLocked() bool {
	var oldestID string
	var oldestT time.Time
	for id, e := range c.entries {
		if e.leases > 0 || e.pendingEvict {
			continue
		}
		if oldestID == "" || e.CreatedAt.Before(oldestT) {
			oldestID, oldestT = id, e.CreatedAt
		}
	}
	if oldestID == "" {
		return false
	}
	old := c.entries[oldestID]
	delete(c.entries, oldestID)
	os.RemoveAll(old.ExtractRoot)
	return true
}

func (c *previewCache) get(id string) *previewEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[id]
	if !ok {
		return nil
	}
	e.CreatedAt = time.Now()
	return e
}

// acquire takes a lease on the entry so its ExtractRoot cannot be removed while
// an install reads it. Returns nil if the entry is gone. Pair with release.
func (c *previewCache) acquire(id string) *previewEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[id]
	if !ok {
		return nil
	}
	e.leases++
	e.CreatedAt = time.Now()
	return e
}

// release drops a lease; if the entry was marked for eviction while leased, the
// final release deletes it and removes its ExtractRoot exactly once.
func (c *previewCache) release(id string) {
	c.mu.Lock()
	var toRemove string
	if e, ok := c.entries[id]; ok {
		if e.leases > 0 {
			e.leases--
		}
		if e.leases == 0 && e.pendingEvict {
			delete(c.entries, id)
			toRemove = e.ExtractRoot
		}
	}
	c.mu.Unlock()
	if toRemove != "" {
		os.RemoveAll(toRemove)
	}
}

func (c *previewCache) discard(id string) bool {
	c.mu.Lock()
	var toRemove string
	e, ok := c.entries[id]
	if ok {
		if e.leases > 0 {
			// An install is still reading it — defer removal to the last release.
			e.pendingEvict = true
			ok = false
		} else {
			delete(c.entries, id)
			toRemove = e.ExtractRoot
		}
	}
	c.mu.Unlock()
	if toRemove != "" {
		os.RemoveAll(toRemove)
		return true
	}
	return ok
}

// sweep evicts expired entries. Called periodically by runPreviewSweeper.
func (c *previewCache) sweep() {
	c.mu.Lock()
	now := time.Now()
	var expired []string
	for id, e := range c.entries {
		if now.Sub(e.CreatedAt) <= c.ttl {
			continue
		}
		if e.leases > 0 {
			e.pendingEvict = true // reclaimed on release
			continue
		}
		expired = append(expired, e.ExtractRoot)
		delete(c.entries, id)
	}
	c.mu.Unlock()
	for _, root := range expired {
		os.RemoveAll(root)
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

// StartDownload enqueues a new download from an NXM URI. Wraps the download
// manager so daemon-level policy (require initialized manager, surface
func (d *Daemon) StartDownload(nxmURI string) (string, int, error) {
	if d.downloadMgr == nil {
		const msg = "NXM ignored: no Nexus API key set — open Settings to add one"
		select {
		case d.statusCh <- ipc.StatusEventResult{Error: msg}:
		default:
		}
		return "", 0, fmt.Errorf("download manager not initialized (set nexus_api_key in config)")
	}
	override := d.resolveActiveGameOverride(nxmURI)
	return d.downloadMgr.StartDownloadForGame(nxmURI, override)
}

// resolveActiveGameOverride decides whether an inbound NXM should be
// routed to the frontend's currently-active game instead of the canonical
func (d *Daemon) resolveActiveGameOverride(nxmURI string) string {
	d.activeGameIDMu.RLock()
	active := d.activeGameID
	d.activeGameIDMu.RUnlock()
	if active == "" {
		return ""
	}
	link, err := download.ParseNXM(nxmURI)
	if err != nil {
		return ""
	}
	defaultGameID, err := link.GameID()
	if err != nil {
		return ""
	}
	if active == defaultGameID {
		return ""
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	gc, ok := d.config.Games[active]
	if !ok {
		return ""
	}
	if gc.LinkedFromGameID != defaultGameID {
		return ""
	}
	return active
}

// SetActiveGame records the frontend's currently-displayed game. Empty
// string clears the hint. See activeGameID field comment for usage.
func (d *Daemon) SetActiveGame(gameID string) error {
	d.activeGameIDMu.Lock()
	d.activeGameID = gameID
	d.activeGameIDMu.Unlock()
	return nil
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

// PreviewInstall extracts an archive into a daemon-cached tmpdir and
// returns either a FOMOD plan for the UI wizard or a flat file listing.
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

func (d *Daemon) StartInstall(req ipc.StartInstallRequest) (string, int, error) {
	if _, ok := d.config.Games[req.GameID]; !ok {
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
			return "", 0, &ipc.ArchiveMissingError{GameID: req.GameID, Path: req.ArchiveRelPath}
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

	// Serialize all writes to this destination mod folder (H-9/H-11) so a
	// concurrent install/reinstall can't interleave a mergeTree with a
	// metadata.yaml rewrite. Acquired before the preview lease (Guard C6).
	defer d.lockMods(req.GameID, target)()

	var extractedRoot string
	if req.PreviewID != "" {
		// Lease the preview so a concurrent sweep/put/discard can't RemoveAll the
		// extract dir while download.Install reads it (H-12). Released below.
		pe := d.previews.acquire(req.PreviewID)
		if pe == nil {
			return "", 0, &ipc.PreviewNotFoundError{PreviewID: req.PreviewID}
		}
		defer d.previews.release(req.PreviewID)
		extractedRoot = pe.ExtractRoot
	}

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
			return "", 0, &ipc.FomodRequiredError{
				GameID: req.GameID, Path: path, PreviewID: req.PreviewID,
			}
		}
		if name, ok := download.IsCollisionMarker(err); ok {
			return "", 0, &ipc.ModCollisionError{Name: name}
		}
		return "", 0, err
	}

	if req.PreviewID != "" {
		d.previews.discard(req.PreviewID)
	}

	d.invalidateInstalledArchiveCache(req.GameID)
	if req.Mode == ipc.InstallAsNewMod {
		d.ensureInModList(req.GameID, result.ModFolder)
	}

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
	// Serialize against installs/uninstalls of either folder (H-9/H-11).
	defer d.lockMods(gameID, oldName, newName)()
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

	meta, _ := download.LoadModMetadata(dst)
	if meta != nil {
		meta.Folder = newName
		if meta.Name == oldName {
			meta.Name = newName
		}
		_ = download.SaveModMetadata(dst, meta)
	}

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
	d.mu.RLock()
	mm, mmOk := d.mountMgrs[gameID]
	ms, msOk := d.mountStates[gameID]
	gc, gcOk := d.config.Games[gameID]
	d.mu.RUnlock()
	if mmOk && msOk && gcOk && mm.IsMounted() {
		if _, entries, err := d.profileMgr.Load(gameID, ms.profileName); err == nil {
			layers := d.buildLayers(gameID, gc, entries)
			// H-6: mark dirty (in-memory only); the on-disk farm is rebuilt
			// before the next launch or on an explicit Apply.
			if err := mm.MarkDirty(layers); err == nil {
				select {
				case d.statusCh <- ipc.StatusEventResult{VFSStatus: d.vfsStatus(gameID, gc, ms.profileName, mm, entries)}:
				default:
				}
			}
		}
	}
	return nil
}

// UninstallModAsync is the async wrapper used for huge mod folders (TTW
func (d *Daemon) UninstallModAsync(gameID, modName string, force bool) ([]string, error) {
	flagged, modDir, err := d.uninstallModSync(gameID, modName, force)
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
		d.invalidateInstalledArchiveCache(gameID)
	}()
	return flagged, nil
}

// uninstallModSync is the shared bookkeeping path for UninstallMod and
// UninstallModAsync. Returns the resolved modDir so the async variant
func (d *Daemon) uninstallModSync(gameID, modName string, force bool) ([]string, string, error) {
	if _, ok := d.config.Games[gameID]; !ok {
		return nil, "", fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	modDir := filepath.Join(config.ModsDir(gameID), modName)
	meta, err := download.LoadModMetadata(modDir)
	if err != nil || meta == nil || (len(meta.SourceArchives) == 0 && meta.Name == "") {
		if _, statErr := os.Stat(modDir); os.IsNotExist(statErr) {
			return nil, "", &ipc.ModNotFoundError{GameID: gameID, Name: modName}
		}
		if err != nil {
			return nil, "", fmt.Errorf("reading mod metadata: %w", err)
		}
	}

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
		return nil, "", &ipc.ModInUseError{Name: modName, Profiles: enabledIn}
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
		if row, err := d.buildArchiveRow(gameID, rel); err == nil {
			d.archiveBus.Publish(gameID, ipc.ArchiveEventResult{
				GameID: gameID, RowChanged: row,
			})
		}
	}

	return flagged, modDir, nil
}

// UninstallMod removes a mod's install dir, strips it from every profile's
func (d *Daemon) UninstallMod(gameID, modName string, force bool) ([]string, error) {
	if _, ok := d.config.Games[gameID]; !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	defer d.lockMods(gameID, modName)()
	modDir := filepath.Join(config.ModsDir(gameID), modName)
	meta, err := download.LoadModMetadata(modDir)
	if err != nil || meta == nil || (len(meta.SourceArchives) == 0 && meta.Name == "") {
		if _, statErr := os.Stat(modDir); os.IsNotExist(statErr) {
			return nil, &ipc.ModNotFoundError{GameID: gameID, Name: modName}
		}
		if err != nil {
			return nil, fmt.Errorf("reading mod metadata: %w", err)
		}
	}

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
		if row, err := d.buildArchiveRow(gameID, rel); err == nil {
			d.archiveBus.Publish(gameID, ipc.ArchiveEventResult{
				GameID: gameID, RowChanged: row,
			})
		}
	}

	d.invalidateInstalledArchiveCache(gameID)

	d.mu.RLock()
	mm, mmOk := d.mountMgrs[gameID]
	ms, msOk := d.mountStates[gameID]
	gc, gcOk := d.config.Games[gameID]
	d.mu.RUnlock()
	if mmOk && msOk && gcOk && mm.IsMounted() {
		if _, entries, err := d.profileMgr.Load(gameID, ms.profileName); err == nil {
			layers := d.buildLayers(gameID, gc, entries)
			// H-6: mark dirty (in-memory only); the on-disk farm is rebuilt
			// before the next launch or on an explicit Apply.
			if err := mm.MarkDirty(layers); err == nil {
				select {
				case d.statusCh <- ipc.StatusEventResult{VFSStatus: d.vfsStatus(gameID, gc, ms.profileName, mm, entries)}:
				default:
				}
			}
		}
	}
	slog.Info("mod uninstalled", "game", gameID, "mod", modName, "archives_flagged", flagged)
	return flagged, nil
}

func (d *Daemon) ReinstallMod(gameID, modName string) (int, int, int, error) {
	if _, ok := d.config.Games[gameID]; !ok {
		return 0, 0, 0, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	defer d.lockMods(gameID, modName)()
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
