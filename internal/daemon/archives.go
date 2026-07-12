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

// managerHooks wires the download manager's callbacks to the per-game stream bus and post-install plumbing.
func (ar *ArchiveService) managerHooks() download.ManagerHooks {
	return download.ManagerHooks{
		OnDownloadProgress: func(snap download.DownloadSnapshot) {
			ar.s.archiveBus.Publish(snap.GameID, dto.ArchiveEventResult{
				GameID: snap.GameID,
				Progress: &dto.DownloadProgressResult{
					DownloadID: snap.ID, ModName: snap.ModName,
					BytesDownloaded: snap.BytesDownloaded, BytesTotal: snap.BytesTotal,
					Status: snap.Status, Error: snap.Error,
					QueuedAhead: snap.QueuedAhead, GameID: snap.GameID,
				},
			})
		},
		OnArchiveLanded: func(snap download.DownloadSnapshot, archivePath string, sidecar download.ArchiveSidecar) {
			if row, err := ar.buildArchiveRow(snap.GameID, relFromDownloads(snap.GameID, archivePath)); err == nil {
				row.DownloadID = snap.ID
				ar.s.archiveBus.Publish(snap.GameID, dto.ArchiveEventResult{
					GameID: snap.GameID, RowChanged: row,
				})
			}
			ar.s.invalidateInstalledArchiveCache(snap.GameID)

			settings, _ := config.LoadGameSettings(snap.GameID)
			if !settings.AutoInstall {
				return
			}
			go ar.autoInstallAfterDownload(snap.GameID, archivePath, sidecar)
		},
	}
}

// relFromDownloads converts an absolute archive path under DownloadsDir into the index-relative form.
func relFromDownloads(gameID, absArchive string) string {
	rel, err := filepath.Rel(config.DownloadsDir(gameID), absArchive)
	if err != nil {
		return absArchive
	}
	return rel
}

// autoInstallAfterDownload is the daemon-side companion to the download manager's auto-install setting.
func (ar *ArchiveService) autoInstallAfterDownload(gameID, archivePath string, sidecar download.ArchiveSidecar) {
	rel := relFromDownloads(gameID, archivePath)
	modName := sidecar.ModName
	if modName == "" {
		base := filepath.Base(archivePath)
		modName = strings.TrimSuffix(base, filepath.Ext(base))
	}
	if _, _, err := ar.s.svc.install.StartInstall(dto.StartInstallRequest{
		GameID: gameID, ArchiveRelPath: rel,
		Mode: dto.InstallAsNewMod, TargetMod: modName,
	}); err != nil {
		slog.Info("auto-install skipped", "archive", rel, "reason", err)
	}
}

// StartDownload enqueues a new download from an NXM URI.
func (ar *ArchiveService) StartDownload(nxmURI string) (string, int, error) {
	if ar.s.downloadMgr == nil {
		const msg = "NXM ignored: no Nexus API key set — open Settings to add one"
		select {
		case ar.s.statusCh <- dto.StatusEventResult{Error: msg}:
		default:
		}
		return "", 0, fmt.Errorf("download manager not initialized (set nexus_api_key in config)")
	}
	override := ar.resolveActiveGameOverride(nxmURI)
	return ar.s.downloadMgr.StartDownloadForGame(nxmURI, override)
}

// resolveActiveGameOverride decides whether an inbound NXM should be routed to the active game.
func (ar *ArchiveService) resolveActiveGameOverride(nxmURI string) string {
	ar.s.activeGameIDMu.RLock()
	active := ar.s.activeGameID
	ar.s.activeGameIDMu.RUnlock()
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
	ar.s.mu.RLock()
	defer ar.s.mu.RUnlock()
	gc, ok := ar.s.config.Games[active]
	if !ok {
		return ""
	}
	if gc.LinkedFromGameID != defaultGameID {
		return ""
	}
	return active
}

func (ar *ArchiveService) CancelDownload(id string) error {
	if ar.s.downloadMgr == nil {
		return &download.DownloadNotFoundError{ID: id}
	}
	return ar.s.downloadMgr.CancelDownload(id)
}

func (ar *ArchiveService) RetryDownload(id string) (int, error) {
	if ar.s.downloadMgr == nil {
		return 0, fmt.Errorf("download manager not initialized")
	}
	return ar.s.downloadMgr.RetryDownload(id)
}

// ListArchives returns the per-game Downloads view.
func (ar *ArchiveService) ListArchives(gameID string) ([]dto.ArchiveRowResult, error) {
	if _, ok := ar.s.config.Games[gameID]; !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	idx, err := download.LoadIndex(gameID)
	if err != nil {
		return nil, err
	}
	installedBy := ar.s.installedArchiveMap(gameID)
	downloadsDir := config.DownloadsDir(gameID)

	rows := make([]dto.ArchiveRowResult, 0, len(idx.Archives))
	for _, e := range idx.Archives {
		absArchive := filepath.Join(downloadsDir, e.Path)
		row := dto.ArchiveRowResult{
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
			row.Status = dto.DownloadStatusInstalled
			row.InstalledModFolder = installRec.Folder
			row.Merged = installRec.Merged
		case !fileExists && ar.s.downloadMgr != nil:
			row.Status = dto.DownloadStatusDownloading
			row.DownloadID = ar.s.downloadMgr.ActiveDownloadIDByArchive(absArchive)
		case fileExists && e.Uninstalled:
			row.Status = dto.DownloadStatusUninstalled
		case fileExists:
			row.Status = dto.DownloadStatusDownloaded
		default:
			row.Status = dto.DownloadStatusUnknown
		}
		if row.DownloadID == "" && ar.s.downloadMgr != nil {
			row.DownloadID = ar.s.downloadMgr.ActiveDownloadIDByArchive(absArchive)
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
			rows = append(rows, dto.ArchiveRowResult{
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

func ledgerToDownloadStatus(ls download.LedgerStatus) dto.DownloadStatus {
	switch ls {
	case download.LedgerQueued:
		return dto.DownloadStatusQueued
	case download.LedgerDownloading:
		return dto.DownloadStatusDownloading
	case download.LedgerDownloaded:
		return dto.DownloadStatusDownloaded
	case download.LedgerCancelled:
		return dto.DownloadStatusCancelled
	case download.LedgerFailed:
		return dto.DownloadStatusFailed
	}
	return dto.DownloadStatusUnknown
}

// RemoveArchive deletes an archive, its sidecar, and the index entry.
func (ar *ArchiveService) RemoveArchive(gameID, archiveRelPath string) error {
	if _, ok := ar.s.config.Games[gameID]; !ok {
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
	ar.s.invalidateInstalledArchiveCache(gameID)
	ar.s.archiveBus.Publish(gameID, dto.ArchiveEventResult{
		GameID: gameID, ArchiveRemoved: archiveRelPath,
	})
	return nil
}

func (ar *ArchiveService) SetArchiveHidden(gameID, archiveRelPath string, hidden bool) error {
	if _, ok := ar.s.config.Games[gameID]; !ok {
		return fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	if err := download.SetHidden(gameID, archiveRelPath, hidden); err != nil {
		return err
	}
	if row, err := ar.buildArchiveRow(gameID, archiveRelPath); err == nil {
		ar.s.archiveBus.Publish(gameID, dto.ArchiveEventResult{GameID: gameID, RowChanged: row})
	}
	return nil
}

func (ar *ArchiveService) SetArchivesHiddenBulk(gameID string, hidden bool, scope dto.BulkHideScope) (int, error) {
	if _, ok := ar.s.config.Games[gameID]; !ok {
		return 0, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	installedBy := ar.s.installedArchiveMap(gameID)
	pred := func(e download.IndexEntry) bool {
		_, isInstalled := installedBy[filepath.Join("Downloads", e.Path)]
		switch scope {
		case dto.BulkHideAll:
			return true
		case dto.BulkHideInstalled:
			return isInstalled
		case dto.BulkHideUninstalled:
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

func (ar *ArchiveService) RefreshArchiveMetadata(gameID, archiveRelPath string) (*dto.ArchiveRowResult, error) {
	gc, ok := ar.s.config.Games[gameID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	_ = gc
	if ar.s.config.NexusAPIKey == "" {
		return nil, fmt.Errorf("nexus API key required — paste one in Tools → Settings")
	}
	downloadsDir := config.DownloadsDir(gameID)
	absArchive := filepath.Join(downloadsDir, archiveRelPath)
	if _, err := os.Stat(absArchive); err != nil {
		return nil, &ArchiveMissingError{GameID: gameID, Path: archiveRelPath}
	}
	sc, err := download.LoadSidecar(absArchive)
	if err != nil || sc == nil || sc.ModID == 0 || sc.GameDomain == "" {
		return nil, fmt.Errorf("sidecar missing the Nexus ids needed to refresh — cannot refresh")
	}
	nx := download.NewNexusClient(ar.s.config.NexusAPIKey)
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
	row, err := ar.buildArchiveRow(gameID, archiveRelPath)
	if err != nil {
		return nil, err
	}
	ar.s.archiveBus.Publish(gameID, dto.ArchiveEventResult{GameID: gameID, RowChanged: row})
	return row, nil
}

// buildArchiveRow composes a single archive row on demand.
func (ar *ArchiveService) buildArchiveRow(gameID, archiveRelPath string) (*dto.ArchiveRowResult, error) {
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
		return nil, &ArchiveMissingError{GameID: gameID, Path: archiveRelPath}
	}

	row := dto.ArchiveRowResult{
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
	installedBy := ar.s.installedArchiveMap(gameID)
	fileExists := false
	if fi, err := os.Stat(absArchive); err == nil && !fi.IsDir() {
		fileExists = true
	}
	lookupKey := filepath.Join("Downloads", archiveRelPath)
	installRec := installedBy[lookupKey]
	switch {
	case installRec.Folder != "":
		row.Status = dto.DownloadStatusInstalled
		row.InstalledModFolder = installRec.Folder
		row.Merged = installRec.Merged
	case fileExists && idxEntry.Uninstalled:
		row.Status = dto.DownloadStatusUninstalled
	case fileExists:
		row.Status = dto.DownloadStatusDownloaded
	default:
		row.Status = dto.DownloadStatusUnknown
	}
	return &row, nil
}

// StreamArchiveEvents subscribes the caller to per-game archive stream events.
func (ar *ArchiveService) StreamArchiveEvents(ctx context.Context, gameID string) (<-chan dto.ArchiveEventResult, error) {
	if _, ok := ar.s.config.Games[gameID]; !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	ch, _ := ar.s.archiveBus.Subscribe(ctx, gameID)
	return ch, nil
}
