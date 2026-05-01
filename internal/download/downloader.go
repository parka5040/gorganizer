package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/ipc"
)

const (
	StatusQueued      = ipc.DownloadStatusQueued
	StatusDownloading = ipc.DownloadStatusDownloading
	StatusDownloaded  = ipc.DownloadStatusDownloaded
	StatusInstalling  = ipc.DownloadStatusInstalling
	StatusInstalled   = ipc.DownloadStatusInstalled
	StatusCancelled   = ipc.DownloadStatusCancelled
	StatusFailed      = ipc.DownloadStatusFailed
)

// Download tracks a single download operation, owned by the Manager.
type Download struct {
	ID              string
	GameID          string
	ModName         string
	NXMURI          string
	ModID           int
	FileID          int
	GameSlug        string
	ArchiveRel      string
	Status          ipc.DownloadStatus
	BytesDownloaded int64
	BytesTotal      int64
	Error           string
	QueuedAhead     int32

	cancel context.CancelFunc
}

// PostInstallHook is invoked after the auto-install path completes a mod.
type PostInstallHook func(gameID, modName string)

// Manager orchestrates the download-extract-install pipeline with bounded concurrency.
type Manager struct {
	nexus       URLResolver
	config      *config.Config
	mu          sync.RWMutex
	active      map[string]*Download
	queued      []*Download
	hooks       ManagerHooks
	postInstall PostInstallHook
	maxConcur   int

	queuePump chan struct{}
	stop      chan struct{}
	stopped   bool
	stopMu    sync.Mutex
}

// ManagerHooks are non-blocking streaming callbacks invoked as downloads progress.
type ManagerHooks struct {
	OnDownloadProgress func(snapshot DownloadSnapshot)
	OnArchiveLanded    func(d DownloadSnapshot, archivePath string, sidecar ArchiveSidecar)
}

// DownloadSnapshot is a lock-free copy of a Download for progress listeners.
type DownloadSnapshot struct {
	ID              string
	GameID          string
	ModName         string
	BytesDownloaded int64
	BytesTotal      int64
	Status          ipc.DownloadStatus
	Error           string
	QueuedAhead     int32
}

// NewManager creates a download Manager; caller must Stop() on shutdown.
func NewManager(nexus URLResolver, cfg *config.Config, maxConcurrent int, hooks ManagerHooks) *Manager {
	if maxConcurrent < 1 {
		maxConcurrent = 3
	}
	m := &Manager{
		nexus:     nexus,
		config:    cfg,
		active:    make(map[string]*Download),
		hooks:     hooks,
		maxConcur: maxConcurrent,
		queuePump: make(chan struct{}, 1),
		stop:      make(chan struct{}),
	}
	go m.runQueuePump()
	return m
}

func (m *Manager) SetPostInstallHook(hook PostInstallHook) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.postInstall = hook
}

// Stop halts the queue pump; active downloads keep running.
func (m *Manager) Stop() {
	m.stopMu.Lock()
	if m.stopped {
		m.stopMu.Unlock()
		return
	}
	m.stopped = true
	close(m.stop)
	m.stopMu.Unlock()
}

// StartDownload enqueues a new download from an NXM URI.
func (m *Manager) StartDownload(uri string) (id string, queuedAhead int, err error) {
	return m.StartDownloadForGame(uri, "")
}

// StartDownloadForGame is StartDownload with an optional gameID override.
func (m *Manager) StartDownloadForGame(uri, overrideGameID string) (id string, queuedAhead int, err error) {
	link, err := ParseNXM(uri)
	if err != nil {
		return "", 0, err
	}
	gameID, err := link.GameID()
	if err != nil {
		return "", 0, err
	}
	if overrideGameID != "" {
		gameID = overrideGameID
	}
	if link.IsExpired(time.Now()) {
		return "", 0, &ipc.NXMExpiredError{URI: uri}
	}

	id = "dl-" + uuid.NewString()
	dl := &Download{
		ID:       id,
		GameID:   gameID,
		GameSlug: link.GameSlug,
		ModID:    link.ModID,
		FileID:   link.FileID,
		NXMURI:   uri,
		Status:   StatusQueued,
	}

	_ = UpsertLedgerEntry(LedgerEntry{
		ID: id, NXMURI: uri, GameID: gameID, GameSlug: link.GameSlug,
		ModID: link.ModID, FileID: link.FileID,
		Status: LedgerQueued, StartedAt: time.Now(),
	})

	m.mu.Lock()
	m.queued = append(m.queued, dl)
	ahead := len(m.queued) + len(m.active) - 1
	if ahead < 0 {
		ahead = 0
	}
	dl.QueuedAhead = int32(ahead)
	m.mu.Unlock()

	m.emitProgress(dl)
	m.signalPump()
	return id, ahead, nil
}

// RetryDownload restarts a failed/cancelled download, resuming from .part if present.
func (m *Manager) RetryDownload(id string) (queuedAhead int, err error) {
	m.mu.Lock()
	if dl, ok := m.active[id]; ok {
		_ = dl
		m.mu.Unlock()
		return 0, nil
	}
	for _, dl := range m.queued {
		if dl.ID == id {
			m.mu.Unlock()
			return int(dl.QueuedAhead), nil
		}
	}
	m.mu.Unlock()

	for gameID := range m.config.Games {
		entries, err := LoadLedger(gameID)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.ID != id {
				continue
			}
			if e.NXMURI == "" {
				return 0, fmt.Errorf("ledger entry %q has no NXM URI; cannot retry", id)
			}
			link, err := ParseNXM(e.NXMURI)
			if err != nil {
				return 0, err
			}
			if link.IsExpired(time.Now()) {
				return 0, &ipc.NXMExpiredError{URI: e.NXMURI}
			}
			dl := &Download{
				ID: e.ID, GameID: e.GameID, GameSlug: e.GameSlug,
				ModID: e.ModID, FileID: e.FileID, ArchiveRel: e.ArchiveRelPath,
				NXMURI: e.NXMURI, Status: StatusQueued,
				BytesDownloaded: e.BytesDone, BytesTotal: e.BytesTotal,
			}
			upd := e
			upd.Status = LedgerQueued
			upd.Error = ""
			_ = UpsertLedgerEntry(upd)

			m.mu.Lock()
			m.queued = append(m.queued, dl)
			ahead := len(m.queued) + len(m.active) - 1
			if ahead < 0 {
				ahead = 0
			}
			dl.QueuedAhead = int32(ahead)
			m.mu.Unlock()

			m.emitProgress(dl)
			m.signalPump()
			return ahead, nil
		}
	}
	return 0, &ipc.DownloadNotFoundError{ID: id}
}

// CancelDownload aborts an active download or de-queues a pending one.
func (m *Manager) CancelDownload(id string) error {
	m.mu.Lock()
	if dl, ok := m.active[id]; ok {
		if dl.cancel != nil {
			dl.cancel()
		}
		m.mu.Unlock()
		return nil
	}
	for i, dl := range m.queued {
		if dl.ID == id {
			m.queued = append(m.queued[:i], m.queued[i+1:]...)
			dl.Status = StatusCancelled
			dl.Error = "cancelled"
			m.mu.Unlock()
			m.emitProgress(dl)
			_ = UpsertLedgerEntry(LedgerEntry{
				ID: id, GameID: dl.GameID, NXMURI: dl.NXMURI,
				GameSlug: dl.GameSlug, ModID: dl.ModID, FileID: dl.FileID,
				ArchiveRelPath: dl.ArchiveRel, BytesDone: dl.BytesDownloaded,
				BytesTotal: dl.BytesTotal, Status: LedgerCancelled,
				Error: "cancelled",
			})
			return nil
		}
	}
	m.mu.Unlock()

	for gameID := range m.config.Games {
		entries, _ := LoadLedger(gameID)
		for _, e := range entries {
			if e.ID == id {
				upd := e
				upd.Status = LedgerCancelled
				upd.Error = "cancelled"
				_ = UpsertLedgerEntry(upd)
				return nil
			}
		}
	}
	return &ipc.DownloadNotFoundError{ID: id}
}

func (m *Manager) GetProgress(downloadID string) (*DownloadSnapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if dl, ok := m.active[downloadID]; ok {
		s := snapshotOf(dl)
		return &s, nil
	}
	for _, dl := range m.queued {
		if dl.ID == downloadID {
			s := snapshotOf(dl)
			return &s, nil
		}
	}
	return nil, &ipc.DownloadNotFoundError{ID: downloadID}
}

// ActiveDownloadIDByArchive returns the live download ID matching an absolute archive path.
func (m *Manager) ActiveDownloadIDByArchive(absArchive string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, dl := range m.active {
		if dl.ArchiveRel == "" {
			continue
		}
		if filepath.Join(config.DownloadsDir(dl.GameID), dl.ArchiveRel) == absArchive {
			return dl.ID
		}
	}
	return ""
}

// ActiveDownloadIDByNXM looks up an in-flight download by NXM URI.
func (m *Manager) ActiveDownloadIDByNXM(uri string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for id, dl := range m.active {
		if dl.NXMURI == uri {
			return id
		}
	}
	for _, dl := range m.queued {
		if dl.NXMURI == uri {
			return dl.ID
		}
	}
	return ""
}

// RehydrateLedger re-enqueues non-terminal ledger entries on daemon startup.
func (m *Manager) RehydrateLedger() {
	for gameID := range m.config.Games {
		entries, err := LoadLedger(gameID)
		if err != nil {
			slog.Warn("could not load ledger", "game", gameID, "err", err)
			continue
		}
		for _, e := range entries {
			if e.Terminal() {
				continue
			}
			if e.NXMURI == "" {
				slog.Warn("ledger entry has no URI; marking failed", "id", e.ID)
				upd := e
				upd.Status = LedgerFailed
				upd.Error = "no nxm_uri in ledger"
				_ = UpsertLedgerEntry(upd)
				continue
			}
			link, parseErr := ParseNXM(e.NXMURI)
			if parseErr != nil || link.IsExpired(time.Now()) {
				slog.Warn("ledger NXM expired; marking failed", "id", e.ID, "uri", e.NXMURI)
				upd := e
				upd.Status = LedgerFailed
				upd.Error = "nxm_expired"
				_ = UpsertLedgerEntry(upd)
				continue
			}
			dl := &Download{
				ID: e.ID, GameID: e.GameID, GameSlug: e.GameSlug,
				ModID: e.ModID, FileID: e.FileID, ArchiveRel: e.ArchiveRelPath,
				NXMURI: e.NXMURI, Status: StatusQueued,
				BytesDownloaded: e.BytesDone, BytesTotal: e.BytesTotal,
			}
			m.mu.Lock()
			m.queued = append(m.queued, dl)
			m.mu.Unlock()
			slog.Info("rehydrated download", "id", e.ID, "bytes_done", e.BytesDone)
		}
	}
	m.signalPump()
}

func (m *Manager) signalPump() {
	select {
	case m.queuePump <- struct{}{}:
	default:
	}
}

func (m *Manager) runQueuePump() {
	for {
		select {
		case <-m.stop:
			return
		case <-m.queuePump:
		}
		for {
			m.mu.Lock()
			if len(m.active) >= m.maxConcur || len(m.queued) == 0 {
				m.mu.Unlock()
				break
			}
			dl := m.queued[0]
			m.queued = m.queued[1:]
			for i, q := range m.queued {
				q.QueuedAhead = int32(len(m.active) + i)
			}
			ctx, cancel := context.WithCancel(context.Background())
			dl.cancel = cancel
			dl.Status = StatusDownloading
			dl.QueuedAhead = 0
			m.active[dl.ID] = dl
			m.mu.Unlock()

			m.emitProgress(dl)
			m.mu.RLock()
			for _, q := range m.queued {
				m.emitProgress(q)
			}
			m.mu.RUnlock()

			go m.runPipeline(ctx, dl)
		}
	}
}

func (m *Manager) runPipeline(ctx context.Context, dl *Download) {
	defer func() {
		m.mu.Lock()
		delete(m.active, dl.ID)
		m.mu.Unlock()
		m.signalPump()
	}()

	link, err := ParseNXM(dl.NXMURI)
	if err != nil {
		m.fail(dl, fmt.Errorf("parsing NXM: %w", err))
		return
	}

	modInfo, err := m.nexus.GetModInfo(link.GameSlug, link.ModID)
	modName := fmt.Sprintf("mod_%d", link.ModID)
	if err == nil && modInfo != nil {
		modName = modInfo.Name
	}
	dl.ModName = modName
	m.emitProgress(dl)

	fileDetails, _ := m.nexus.GetFileDetails(link.GameSlug, link.ModID, link.FileID)

	archiveFilename := pickArchiveFilename(fileDetails, "", link)
	folder := fmt.Sprintf("%d_%s", link.ModID, SanitizeForFolder(modName))
	if strings.TrimSpace(modName) == "" {
		folder = fmt.Sprintf("%d", link.ModID)
	}
	if dl.ArchiveRel == "" {
		dl.ArchiveRel = filepath.Join(folder, archiveFilename)
	}
	downloadsDir := config.DownloadsDir(dl.GameID)
	archivePath := filepath.Join(downloadsDir, dl.ArchiveRel)
	if err := os.MkdirAll(filepath.Dir(archivePath), 0755); err != nil {
		m.fail(dl, fmt.Errorf("creating archive dir: %w", err))
		return
	}
	partPath := PartPath(archivePath)

	cdnURL, err := m.nexus.ResolveDownloadURL(link)
	if err != nil {
		m.fail(dl, fmt.Errorf("resolving CDN URL: %w", err))
		return
	}

	var resumeFrom int64
	if fi, statErr := os.Stat(partPath); statErr == nil {
		resumeFrom = fi.Size()
		dl.BytesDownloaded = resumeFrom
	}

	_ = UpsertLedgerEntry(LedgerEntry{
		ID: dl.ID, GameID: dl.GameID, NXMURI: dl.NXMURI,
		GameSlug: dl.GameSlug, ModID: dl.ModID, FileID: dl.FileID,
		ArchiveRelPath: dl.ArchiveRel,
		BytesDone: dl.BytesDownloaded, BytesTotal: dl.BytesTotal,
		Status: LedgerDownloading,
	})

	if err := m.streamToFile(ctx, cdnURL, partPath, resumeFrom, dl); err != nil {
		if errors.Is(err, context.Canceled) {
			dl.Status = StatusCancelled
			dl.Error = "cancelled"
			m.emitProgress(dl)
			_ = UpsertLedgerEntry(LedgerEntry{
				ID: dl.ID, GameID: dl.GameID, NXMURI: dl.NXMURI,
				GameSlug: dl.GameSlug, ModID: dl.ModID, FileID: dl.FileID,
				ArchiveRelPath: dl.ArchiveRel,
				BytesDone: dl.BytesDownloaded, BytesTotal: dl.BytesTotal,
				Status: LedgerCancelled, Error: "cancelled",
			})
			os.Remove(partPath)
			return
		}
		m.fail(dl, err)
		return
	}

	if err := os.Rename(partPath, archivePath); err != nil {
		m.fail(dl, fmt.Errorf("renaming .part: %w", err))
		return
	}

	relArchive := dl.ArchiveRel
	sidecar := ArchiveSidecar{
		ModID:           link.ModID,
		ModName:         modName,
		GameDomain:      link.GameSlug,
		FileID:          link.FileID,
		FileArchiveName: archiveFilename,
		SizeBytes:       dl.BytesDownloaded,
	}
	if modInfo != nil {
		sidecar.ThumbnailURL = modInfo.PictureURL
		sidecar.AdultContent = modInfo.ContainsAdult
	}
	if fileDetails != nil {
		sidecar.FileName = fileDetails.Name
		sidecar.Version = fileDetails.Version
		sidecar.Category = NormalizeCategory(fileDetails.CategoryName)
		sidecar.UploadedAt = fileDetails.UploadedTime
	}
	if err := SaveSidecar(archivePath, sidecar, time.Now()); err != nil {
		slog.Warn("writing sidecar failed", "err", err)
	}
	if err := UpsertEntry(dl.GameID, IndexEntry{
		Path: relArchive, ModID: link.ModID, FileID: link.FileID,
	}); err != nil {
		slog.Warn("updating downloads index failed", "err", err)
	}

	_ = RemoveLedgerEntry(dl.GameID, dl.ID)

	dl.Status = StatusDownloaded
	m.emitProgress(dl)

	if m.hooks.OnArchiveLanded != nil {
		m.hooks.OnArchiveLanded(snapshotOf(dl), archivePath, sidecar)
	}

	slog.Info("download complete", "name", modName, "game", dl.GameID,
		"archive", archivePath, "bytes", dl.BytesDownloaded)
}

// streamToFile GETs cdnURL with optional resume Range header and writes to destPath.
func (m *Manager) streamToFile(ctx context.Context, cdnURL, destPath string, resumeFrom int64, dl *Download) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cdnURL, nil)
	if err != nil {
		return err
	}
	if resumeFrom > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeFrom))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDownloadFailed, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		if resumeFrom > 0 {
			slog.Warn("server ignored Range header; restarting from 0", "url", cdnURL)
			resumeFrom = 0
			_ = os.Truncate(destPath, 0)
			dl.BytesDownloaded = 0
		}
	case http.StatusPartialContent:
	default:
		return fmt.Errorf("%w: HTTP %d", ErrDownloadFailed, resp.StatusCode)
	}

	if cl := resp.ContentLength; cl > 0 {
		dl.BytesTotal = cl + resumeFrom
	}

	var out *os.File
	if resumeFrom > 0 {
		out, err = os.OpenFile(destPath, os.O_WRONLY|os.O_APPEND, 0644)
	} else {
		out, err = os.Create(destPath)
	}
	if err != nil {
		return err
	}
	defer out.Close()

	buf := make([]byte, 64*1024)
	var lastLedger time.Time
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := out.Write(buf[:n]); writeErr != nil {
				return writeErr
			}
			dl.BytesDownloaded += int64(n)
			m.emitProgress(dl)
			if time.Since(lastLedger) > time.Second {
				lastLedger = time.Now()
				_ = UpsertLedgerEntry(LedgerEntry{
					ID: dl.ID, GameID: dl.GameID, NXMURI: dl.NXMURI,
					GameSlug: dl.GameSlug, ModID: dl.ModID, FileID: dl.FileID,
					ArchiveRelPath: dl.ArchiveRel,
					BytesDone:      dl.BytesDownloaded, BytesTotal: dl.BytesTotal,
					Status: LedgerDownloading,
				})
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return nil
			}
			return readErr
		}
	}
}

func (m *Manager) fail(dl *Download, err error) {
	dl.Status = StatusFailed
	dl.Error = err.Error()
	m.emitProgress(dl)
	slog.Error("download failed", "id", dl.ID, "err", err)
	_ = UpsertLedgerEntry(LedgerEntry{
		ID: dl.ID, GameID: dl.GameID, NXMURI: dl.NXMURI,
		GameSlug: dl.GameSlug, ModID: dl.ModID, FileID: dl.FileID,
		ArchiveRelPath: dl.ArchiveRel,
		BytesDone: dl.BytesDownloaded, BytesTotal: dl.BytesTotal,
		Status: LedgerFailed, Error: err.Error(),
	})
}

func (m *Manager) emitProgress(dl *Download) {
	if m.hooks.OnDownloadProgress != nil {
		m.hooks.OnDownloadProgress(snapshotOf(dl))
	}
}

func snapshotOf(dl *Download) DownloadSnapshot {
	return DownloadSnapshot{
		ID:              dl.ID,
		GameID:          dl.GameID,
		ModName:         dl.ModName,
		BytesDownloaded: dl.BytesDownloaded,
		BytesTotal:      dl.BytesTotal,
		Status:          dl.Status,
		Error:           dl.Error,
		QueuedAhead:     dl.QueuedAhead,
	}
}

// pickArchiveFilename chooses the on-disk filename for an archive.
func pickArchiveFilename(details *NexusFileDetails, downloadURL string, link *NXMLink) string {
	if details != nil && details.FileName != "" {
		return details.FileName
	}
	if u, err := neturl.Parse(downloadURL); err == nil {
		base := filepath.Base(u.Path)
		if base != "" && base != "." && base != "/" {
			return base
		}
	}
	return fmt.Sprintf("%d_%d.archive", link.ModID, link.FileID)
}

// IsExpired is true when the NXM URI's expires timestamp has passed.
func (l *NXMLink) IsExpired(now time.Time) bool {
	if l.Expires == 0 {
		return false
	}
	return now.Unix() >= l.Expires
}

// nexusModPageURL returns the canonical Nexus Mods URL for a mod.
func nexusModPageURL(gameDomain string, modID int) string {
	if gameDomain == "" || modID <= 0 {
		return ""
	}
	return fmt.Sprintf("https://www.nexusmods.com/%s/mods/%d", gameDomain, modID)
}
