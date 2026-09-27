package daemon

import (
	"context"
	"errors"
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
			if ar.s.svc.modDeps != nil {
				ar.s.svc.modDeps.observeDownload(snap)
			}
		},
		OnArchiveLanded: func(snap download.DownloadSnapshot, archivePath string, sidecar download.ArchiveSidecar) {
			if row, err := ar.buildArchiveRow(snap.GameID, relFromDownloads(snap.GameID, archivePath)); err == nil {
				row.DownloadID = snap.ID
				ar.s.archiveBus.Publish(snap.GameID, dto.ArchiveEventResult{
					GameID: snap.GameID, RowChanged: row,
				})
			}
			ar.s.invalidateInstalledArchiveCache(snap.GameID)
			ar.s.goBackground("landed archive", func() { ar.handleLandedArchive(snap, archivePath, sidecar) })
		},
	}
}

// handleLandedArchive waits for startup recovery, then installs a landed archive for the dependency requests it satisfies, otherwise auto-installs it when the game's setting is on.
func (ar *ArchiveService) handleLandedArchive(snap download.DownloadSnapshot, archivePath string, sidecar download.ArchiveSidecar) {
	if err := ar.s.awaitRecovery(); err != nil {
		slog.Warn("handling a landed archive skipped; the next start consumes it for waiting dependency requests but never auto-installs it", "game", snap.GameID, "archive", archivePath, "err", err)
		return
	}
	landing := heldLanding{snap: snap, path: archivePath, sidecar: sidecar}
	if ar.s.holdDeferredLanding(landing) {
		return
	}
	var release func()
	for {
		var err error
		release, err = ar.s.acquireShared(snap.GameID, "install")
		if err == nil {
			break
		}
		var busy *dto.OperationBusyError
		if !errors.As(err, &busy) || busy.Operation != "recovery" {
			return
		}
		select {
		case <-ar.s.shutdownCh:
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
	defer release()
	if ar.s.holdDeferredLanding(landing) {
		return
	}
	ar.s.mu.RLock()
	pending := ar.s.recoveryPendingFor(snap.GameID)
	ar.s.mu.RUnlock()
	if pending != nil {
		return
	}
	if deps := ar.s.svc.modDeps; deps != nil && deps.consumeLandedArchive(snap.GameID, snap.ID, archivePath, sidecar) {
		return
	}
	settings, _ := config.LoadGameSettings(snap.GameID)
	if settings.AutoInstall {
		ar.autoInstallAfterDownload(snap.GameID, archivePath, sidecar)
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
	manager := ar.s.downloadStateSnapshot().manager
	if manager == nil {
		const msg = "NXM ignored: no Nexus API key set — open Settings to add one"
		ar.s.publishGuarded(dto.StatusEventResult{Error: msg})
		return "", 0, fmt.Errorf("download manager not initialized (set nexus_api_key in config)")
	}
	override := ar.resolveActiveGameOverride(nxmURI)
	manager = ar.s.downloadStateSnapshot().manager
	if manager == nil {
		const msg = "NXM ignored: no Nexus API key set — open Settings to add one"
		ar.s.publishGuarded(dto.StatusEventResult{Error: msg})
		return "", 0, fmt.Errorf("download manager not initialized (set nexus_api_key in config)")
	}
	return manager.StartDownloadForGame(nxmURI, override)
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
	state := ar.s.downloadStateSnapshot()
	if state.manager == nil {
		return &download.DownloadNotFoundError{ID: id}
	}
	return state.manager.CancelDownload(id, state.gameIDs)
}

func (ar *ArchiveService) RetryDownload(id string) (int, error) {
	state := ar.s.downloadStateSnapshot()
	if state.manager == nil {
		return 0, fmt.Errorf("download manager not initialized")
	}
	return state.manager.RetryDownload(id, state.gameIDs)
}

// ListArchives returns the per-game Downloads view.
func (ar *ArchiveService) ListArchives(gameID string) ([]dto.ArchiveRowResult, error) {
	if !ar.s.gameConfigured(gameID) {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	manager := ar.s.downloadStateSnapshot().manager
	idx, err := download.LoadIndex(gameID)
	if err != nil {
		return nil, err
	}
	installedBy := ar.s.installedArchiveMap(gameID)
	downloadsDir := config.DownloadsDir(gameID)

	rows := make([]dto.ArchiveRowResult, 0, len(idx.Archives))
	for _, e := range idx.Archives {
		absArchive, err := archivePath(downloadsDir, e.Path)
		if err != nil {
			return nil, err
		}
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
		case !fileExists && manager != nil:
			row.Status = dto.DownloadStatusDownloading
			row.DownloadID = manager.ActiveDownloadIDByArchive(absArchive)
		case fileExists && e.Uninstalled:
			row.Status = dto.DownloadStatusUninstalled
		case fileExists:
			row.Status = dto.DownloadStatusDownloaded
		default:
			row.Status = dto.DownloadStatusUnknown
		}
		if row.DownloadID == "" && manager != nil {
			row.DownloadID = manager.ActiveDownloadIDByArchive(absArchive)
		}
		rows = append(rows, row)
	}

	indexed := make(map[string]int, len(idx.Archives))
	for i, e := range idx.Archives {
		indexed[e.Path] = i
	}
	if entries, err := download.LoadLedger(gameID); err == nil {
		for _, le := range entries {
			if i, dup := indexed[le.ArchiveRelPath]; dup {
				if !le.Terminal() || le.Status == download.LedgerFailed || le.Status == download.LedgerCancelled {
					rows[i].DownloadID = le.ID
					rows[i].Status = ledgerToDownloadStatus(le.Status)
					rows[i].BytesDownloaded = le.BytesDone
					if rows[i].GameDomain == "" {
						rows[i].GameDomain = le.GameSlug
					}
				}
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

// RemoveArchive removes a terminal download by ID and optionally deletes its archive and index entry.
func (ar *ArchiveService) RemoveArchive(gameID, archiveRelPath, downloadID string) error {
	if !ar.s.gameConfigured(gameID) {
		return fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	if archiveRelPath == "" && downloadID == "" {
		return &UnsafePathError{Field: "archive_rel_path"}
	}
	var absArchive string
	if archiveRelPath != "" {
		var err error
		absArchive, err = archivePath(config.DownloadsDir(gameID), archiveRelPath)
		if err != nil {
			return err
		}
	}
	if downloadID != "" {
		manager := ar.s.downloadStateSnapshot().manager
		if manager != nil && (manager.IsActive(downloadID) ||
			(absArchive != "" && manager.ActiveDownloadIDByArchive(absArchive) != "")) {
			return download.ErrArchiveDownloadBusy
		}
		entries, err := download.LoadLedger(gameID)
		if err != nil {
			return err
		}
		var match *download.LedgerEntry
		for i := range entries {
			if entries[i].ID == downloadID {
				match = &entries[i]
				break
			}
		}
		switch {
		case match == nil || match.Status == download.LedgerDownloaded:
			if archiveRelPath == "" {
				return &download.DownloadNotFoundError{ID: downloadID}
			}
		case match.Status == download.LedgerQueued || match.Status == download.LedgerDownloading:
			return download.ErrArchiveDownloadBusy
		default:
			if err := download.RemoveLedgerEntry(gameID, downloadID); err != nil {
				return err
			}
		}
	}
	if archiveRelPath == "" {
		return nil
	}
	_ = os.Remove(absArchive)
	_ = os.Remove(download.SidecarPath(absArchive))
	_ = os.Remove(download.PartPath(absArchive))
	if err := download.RemoveEntry(gameID, archiveRelPath); err != nil {
		return err
	}
	entries, err := download.LoadLedger(gameID)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.ArchiveRelPath == archiveRelPath {
			if err := download.RemoveLedgerEntry(gameID, entry.ID); err != nil {
				return err
			}
		}
	}
	ar.s.invalidateInstalledArchiveCache(gameID)
	ar.s.archiveBus.Publish(gameID, dto.ArchiveEventResult{
		GameID: gameID, ArchiveRemoved: archiveRelPath,
	})
	return nil
}

func (ar *ArchiveService) SetArchiveHidden(gameID, archiveRelPath string, hidden bool) error {
	if !ar.s.gameConfigured(gameID) {
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
	if !ar.s.gameConfigured(gameID) {
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
	if !ar.s.gameConfigured(gameID) {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	key := ar.s.nexusAPIKey()
	if key == "" {
		return nil, fmt.Errorf("nexus API key required — paste one in Tools → Settings")
	}
	downloadsDir := config.DownloadsDir(gameID)
	absArchive, err := archivePath(downloadsDir, archiveRelPath)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(absArchive); err != nil {
		return nil, &ArchiveMissingError{GameID: gameID, Path: archiveRelPath}
	}
	sc, err := download.LoadSidecar(absArchive)
	if err != nil || sc == nil || sc.ModID == 0 || sc.GameDomain == "" {
		return nil, fmt.Errorf("sidecar missing the Nexus ids needed to refresh — cannot refresh")
	}
	nx := newNexusDownloadClient(key)
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
	absArchive, err := archivePath(downloadsDir, archiveRelPath)
	if err != nil {
		return nil, err
	}

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
	if !ar.s.gameConfigured(gameID) {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	ch, _ := ar.s.archiveBus.Subscribe(ctx, gameID)
	return ch, nil
}
