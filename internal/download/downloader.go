package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/httpx"
)

const (
	StatusQueued      = dto.DownloadStatusQueued
	StatusDownloading = dto.DownloadStatusDownloading
	StatusDownloaded  = dto.DownloadStatusDownloaded
	StatusInstalling  = dto.DownloadStatusInstalling
	StatusInstalled   = dto.DownloadStatusInstalled
	StatusCancelled   = dto.DownloadStatusCancelled
	StatusFailed      = dto.DownloadStatusFailed
)

type Download struct {
	ID              string
	GameID          string
	ModName         string
	NXMURI          string
	ModID           int
	FileID          int
	GameSlug        string
	ArchiveRel      string
	Status          dto.DownloadStatus
	BytesDownloaded int64
	BytesTotal      int64
	Error           string
	QueuedAhead     int32

	cancel context.CancelFunc
}

type archivePart interface {
	Write([]byte) (int, error)
	Truncate(int64) error
	Sync() error
	Close() error
}

type Manager struct {
	nexus        URLResolver
	httpClient   *http.Client
	openPart     func(string, string, bool) (archivePart, error)
	mu           sync.RWMutex
	active       map[string]*Download
	queued       []*Download
	retrying     map[string]bool
	destinations map[string]string
	hooks        ManagerHooks
	maxConcur    int

	queuePump chan struct{}
	stop      chan struct{}
	stopped   bool
	stopMu    sync.Mutex
}

type ManagerHooks struct {
	OnDownloadProgress func(snapshot DownloadSnapshot)
	OnArchiveLanded    func(d DownloadSnapshot, archivePath string, sidecar ArchiveSidecar)
}

type DownloadSnapshot struct {
	ID              string
	GameID          string
	ModName         string
	BytesDownloaded int64
	BytesTotal      int64
	Status          dto.DownloadStatus
	Error           string
	QueuedAhead     int32
}

// NewManager creates a download Manager; caller must Stop() on shutdown.
func NewManager(nexus URLResolver, maxConcurrent int, hooks ManagerHooks) *Manager {
	return NewManagerWithClient(nexus, maxConcurrent, hooks, httpx.DownloadClient())
}

// NewManagerWithClient creates a download Manager with the supplied HTTP client.
func NewManagerWithClient(nexus URLResolver, maxConcurrent int, hooks ManagerHooks, client *http.Client) *Manager {
	if client == nil {
		client = httpx.DownloadClient()
	}
	if maxConcurrent < 1 {
		maxConcurrent = 3
	}
	m := &Manager{
		nexus:        nexus,
		httpClient:   client,
		active:       make(map[string]*Download),
		retrying:     make(map[string]bool),
		destinations: make(map[string]string),
		hooks:        hooks,
		maxConcur:    maxConcurrent,
		queuePump:    make(chan struct{}, 1),
		stop:         make(chan struct{}),
	}
	go m.runQueuePump()
	return m
}

// SetResolver changes the resolver used by pipelines started after the change.
func (m *Manager) SetResolver(r URLResolver) {
	m.mu.Lock()
	m.nexus = r
	m.mu.Unlock()
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
		return "", 0, &NXMExpiredError{URI: redactURL(uri)}
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
	state := *dl
	m.mu.Unlock()

	m.emitProgress(state)
	m.signalPump()
	return id, ahead, nil
}

// RetryDownload restarts a failed/cancelled download, returning its live position or refusing a concurrent admission.
func (m *Manager) RetryDownload(id string, gameIDs []string) (queuedAhead int, err error) {
	m.mu.Lock()
	if _, ok := m.active[id]; ok {
		m.mu.Unlock()
		return 0, nil
	}
	for _, dl := range m.queued {
		if dl.ID == id {
			ahead := dl.QueuedAhead
			m.mu.Unlock()
			return int(ahead), nil
		}
	}
	if m.retrying[id] {
		m.mu.Unlock()
		return 0, ErrRetryInProgress
	}
	if m.retrying == nil {
		m.retrying = make(map[string]bool)
	}
	m.retrying[id] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.retrying, id)
		m.mu.Unlock()
	}()

	for _, gameID := range gameIDs {
		entries, err := LoadLedger(gameID)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.ID != id {
				continue
			}
			if err := validateLedgerDestination(gameID, e); err != nil {
				m.rejectLedgerEntry(gameID, e, err)
				return 0, err
			}
			if e.NXMURI == "" {
				return 0, fmt.Errorf("ledger entry %q has no NXM URI; cannot retry", id)
			}
			link, err := ParseNXM(e.NXMURI)
			if err != nil {
				return 0, err
			}
			if link.IsExpired(time.Now()) {
				return 0, &NXMExpiredError{URI: redactURL(e.NXMURI)}
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
			state := *dl
			m.mu.Unlock()

			m.emitProgress(state)
			m.signalPump()
			return ahead, nil
		}
	}
	return 0, &DownloadNotFoundError{ID: id}
}

// CancelDownload aborts an active download or de-queues a pending one.
func (m *Manager) CancelDownload(id string, gameIDs []string) error {
	m.mu.Lock()
	if dl, ok := m.active[id]; ok {
		cancel := dl.cancel
		m.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return nil
	}
	for i, dl := range m.queued {
		if dl.ID == id {
			m.queued = append(m.queued[:i], m.queued[i+1:]...)
			dl.Status = StatusCancelled
			dl.Error = "cancelled"
			state := *dl
			m.mu.Unlock()
			m.emitProgress(state)
			_ = UpsertLedgerEntry(LedgerEntry{
				ID: id, GameID: state.GameID, NXMURI: state.NXMURI,
				GameSlug: state.GameSlug, ModID: state.ModID, FileID: state.FileID,
				ArchiveRelPath: state.ArchiveRel, BytesDone: state.BytesDownloaded,
				BytesTotal: state.BytesTotal, Status: LedgerCancelled,
				Error: "cancelled",
			})
			return nil
		}
	}
	m.mu.Unlock()

	for _, gameID := range gameIDs {
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
	return &DownloadNotFoundError{ID: id}
}

// IsActive reports whether a download is running, queued, or being retried.
func (m *Manager) IsActive(id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.active[id]; ok || m.retrying[id] {
		return true
	}
	for _, dl := range m.queued {
		if dl.ID == id {
			return true
		}
	}
	return false
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
	return nil, &DownloadNotFoundError{ID: downloadID}
}

// ActiveDownloadIDByArchive returns the live download ID matching an absolute archive path.
func (m *Manager) ActiveDownloadIDByArchive(absArchive string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, dl := range m.active {
		if dl.ArchiveRel == "" {
			continue
		}
		path, err := resolveArchiveDestination(dl.GameID, dl.ArchiveRel)
		if err == nil && path == absArchive {
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
func (m *Manager) RehydrateLedger(gameIDs []string) {
	for _, gameID := range gameIDs {
		entries, err := LoadLedger(gameID)
		if err != nil {
			slog.Warn("could not load ledger", "game", gameID, "err", err)
			continue
		}
		for _, e := range entries {
			if e.Terminal() {
				continue
			}
			if err := validateLedgerDestination(gameID, e); err != nil {
				m.rejectLedgerEntry(gameID, e, err)
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
				slog.Warn("ledger NXM expired; marking failed", "id", e.ID, "uri", redactURL(e.NXMURI))
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

// rejectLedgerEntry records a refused destination without changing its persisted path.
func (m *Manager) rejectLedgerEntry(gameID string, e LedgerEntry, err error) {
	e.GameID = gameID
	e.Status = LedgerFailed
	e.Error = err.Error()
	if saveErr := UpsertLedgerEntry(e); saveErr != nil {
		slog.Warn("could not mark download failed", "id", e.ID, "err", saveErr)
	}
	m.emitProgress(Download{ID: e.ID, GameID: gameID, Status: StatusFailed, Error: e.Error})
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
			snapshots := []Download{*dl}
			for _, q := range m.queued {
				snapshots = append(snapshots, *q)
			}
			m.mu.Unlock()

			for _, state := range snapshots {
				m.emitProgress(state)
			}
			go m.runPipeline(ctx, dl)
		}
	}
}

func (m *Manager) runPipeline(ctx context.Context, dl *Download) {
	m.mu.RLock()
	resolver := m.nexus
	m.mu.RUnlock()
	defer func() {
		m.mu.Lock()
		delete(m.active, dl.ID)
		m.mu.Unlock()
		m.signalPump()
	}()

	state := m.snapshot(dl)
	link, err := ParseNXM(state.NXMURI)
	if err != nil {
		m.fail(dl, fmt.Errorf("parsing NXM: %w", err))
		return
	}

	modInfo, err := resolver.GetModInfo(link.GameSlug, link.ModID)
	modName := fmt.Sprintf("mod_%d", link.ModID)
	if err == nil && modInfo != nil {
		modName = modInfo.Name
	}
	state = m.update(dl, func(d *Download) { d.ModName = modName })
	m.emitProgress(state)

	fileDetails, _ := resolver.GetFileDetails(link.GameSlug, link.ModID, link.FileID)
	cdnURL, err := resolver.ResolveDownloadURL(link)
	if err != nil {
		m.fail(dl, fmt.Errorf("resolving CDN URL: %w", redactHTTPError(err)))
		return
	}

	restartFromZero := state.ArchiveRel != "" && state.BytesDownloaded == 0
	archiveFilename := pickArchiveFilename(fileDetails, cdnURL, link)
	folder := fmt.Sprintf("%d_%s", link.ModID, SanitizeForFolder(modName))
	if strings.TrimSpace(modName) == "" {
		folder = fmt.Sprintf("%d", link.ModID)
	}
	if state.ArchiveRel == "" {
		state = m.update(dl, func(d *Download) { d.ArchiveRel = filepath.Join(folder, archiveFilename) })
	}
	archivePath, err := resolveArchiveDestination(state.GameID, state.ArchiveRel)
	if err != nil {
		m.fail(dl, err)
		return
	}
	if !m.claimDestination(state.GameID, state.ArchiveRel, state.ID) {
		m.fail(dl, ErrArchiveDownloadBusy)
		return
	}
	defer m.releaseDestination(state.GameID, state.ArchiveRel, state.ID)
	if err := ensureArchiveFolder(archivePath, state.ArchiveRel); err != nil {
		m.fail(dl, err)
		return
	}
	partPath := PartPath(archivePath)
	resumeFrom, err := partSize(partPath, state.ArchiveRel)
	if err != nil {
		m.fail(dl, err)
		return
	}
	if restartFromZero {
		resumeFrom = 0
	}
	state = m.update(dl, func(d *Download) { d.BytesDownloaded = resumeFrom })

	_ = UpsertLedgerEntry(LedgerEntry{
		ID: state.ID, GameID: state.GameID, NXMURI: state.NXMURI,
		GameSlug: state.GameSlug, ModID: state.ModID, FileID: state.FileID,
		ArchiveRelPath: state.ArchiveRel,
		BytesDone:      state.BytesDownloaded, BytesTotal: state.BytesTotal,
		Status: LedgerDownloading,
	})

	if err := m.streamToFile(ctx, cdnURL, partPath, resumeFrom, dl); err != nil {
		var saveErr *ArchiveSaveError
		if errors.Is(err, context.Canceled) && !errors.As(err, &saveErr) {
			state = m.update(dl, func(d *Download) {
				d.Status = StatusCancelled
				d.Error = "cancelled"
			})
			m.emitProgress(state)
			_ = UpsertLedgerEntry(LedgerEntry{
				ID: state.ID, GameID: state.GameID, NXMURI: state.NXMURI,
				GameSlug: state.GameSlug, ModID: state.ModID, FileID: state.FileID,
				ArchiveRelPath: state.ArchiveRel,
				BytesDone:      state.BytesDownloaded, BytesTotal: state.BytesTotal,
				Status: LedgerCancelled, Error: "cancelled",
			})
			if checkArchiveFolder(archivePath, state.ArchiveRel) == nil {
				_ = os.Remove(partPath)
			}
			return
		}
		m.fail(dl, err)
		return
	}

	state = m.snapshot(dl)
	if err := checkArchiveFolder(archivePath, state.ArchiveRel); err != nil {
		m.fail(dl, err)
		return
	}
	partBytes, err := partSize(partPath, state.ArchiveRel)
	if err != nil {
		m.fail(dl, err)
		return
	}
	if partBytes != state.BytesDownloaded || state.BytesTotal > 0 && partBytes != state.BytesTotal {
		m.fail(dl, fmt.Errorf("%w: incomplete archive part", ErrDownloadFailed))
		return
	}
	if err := os.Rename(partPath, archivePath); err != nil {
		m.fail(dl, fmt.Errorf("renaming .part: %w", err))
		return
	}
	if err := atomicfile.SyncDir(filepath.Dir(archivePath)); err != nil {
		slog.Warn("syncing download directory failed", "err", err)
	}

	relArchive := state.ArchiveRel
	sidecar := ArchiveSidecar{
		ModID:           link.ModID,
		ModName:         modName,
		GameDomain:      link.GameSlug,
		FileID:          link.FileID,
		FileArchiveName: filepath.Base(archivePath),
		SizeBytes:       state.BytesDownloaded,
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
	if err := checkArchiveFolder(archivePath, state.ArchiveRel); err != nil {
		m.fail(dl, err)
		return
	}
	if err := SaveSidecar(archivePath, sidecar, time.Now()); err != nil {
		slog.Warn("writing sidecar failed", "err", err)
	}
	if err := UpsertEntry(state.GameID, IndexEntry{
		Path: relArchive, ModID: link.ModID, FileID: link.FileID,
	}); err != nil {
		slog.Warn("updating downloads index failed", "err", err)
	}

	_ = RemoveLedgerEntry(state.GameID, state.ID)

	state = m.update(dl, func(d *Download) { d.Status = StatusDownloaded })
	m.emitProgress(state)

	if m.hooks.OnArchiveLanded != nil {
		m.hooks.OnArchiveLanded(snapshotOf(&state), archivePath, sidecar)
	}

	slog.Info("download complete", "name", modName, "game", state.GameID,
		"archive", archivePath, "bytes", state.BytesDownloaded)
}

// streamToFile GETs cdnURL with optional resume Range header and saves a complete part.
func (m *Manager) streamToFile(ctx context.Context, cdnURL, destPath string, resumeFrom int64, dl *Download) (result error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cdnURL, nil)
	if err != nil {
		return redactHTTPError(err)
	}
	if resumeFrom > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeFrom))
	}
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrDownloadFailed, redactHTTPError(err))
	}
	if resumeFrom > 0 && resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		resp.Body.Close()
		req.Header.Del("Range")
		resumeFrom = 0
		resp, err = m.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrDownloadFailed, redactHTTPError(err))
		}
	}
	defer resp.Body.Close()
	state := m.snapshot(dl)
	var rangeTotal, rangeEnd int64
	switch resp.StatusCode {
	case http.StatusOK:
		if resumeFrom > 0 {
			slog.Warn("server ignored Range header; restarting from 0", "url", redactURL(cdnURL))
		}
		resumeFrom = 0
	case http.StatusPartialContent:
		if resumeFrom == 0 {
			return fmt.Errorf("%w: unexpected partial response", ErrDownloadFailed)
		}
		rangeTotal, rangeEnd, err = parseDownloadRange(resp.Header.Get("Content-Range"), resumeFrom)
		if err == nil && resp.ContentLength > 0 && resp.ContentLength-1 != rangeEnd-resumeFrom {
			err = fmt.Errorf("range length does not match response length")
		}
		if err != nil {
			m.update(dl, func(d *Download) { d.BytesDownloaded, d.BytesTotal = 0, 0 })
			return fmt.Errorf("%w: invalid Content-Range: %w", ErrDownloadFailed, err)
		}
	default:
		return fmt.Errorf("%w: HTTP %d", ErrDownloadFailed, resp.StatusCode)
	}

	expected := int64(-1)
	if resp.ContentLength > 0 || resp.ContentLength == 0 && resp.Header.Get("Content-Length") == "0" {
		if resp.ContentLength > math.MaxInt64-resumeFrom {
			return fmt.Errorf("%w: invalid response length", ErrDownloadFailed)
		}
		expected = resp.ContentLength + resumeFrom
	}
	if rangeTotal > 0 {
		expected = rangeTotal
	}
	state = m.update(dl, func(d *Download) {
		d.BytesDownloaded = resumeFrom
		d.BytesTotal = 0
		if expected >= 0 {
			d.BytesTotal = expected
		}
	})

	if err := checkArchiveFolder(destPath, state.ArchiveRel); err != nil {
		return err
	}
	open := m.openPart
	if open == nil {
		open = func(path, rel string, appendData bool) (archivePart, error) {
			return openArchivePart(path, rel, appendData)
		}
	}
	out, err := open(destPath, state.ArchiveRel, resumeFrom > 0)
	if err != nil {
		return err
	}
	defer func() {
		if err := out.Close(); err != nil {
			var saveErr *ArchiveSaveError
			if errors.As(result, &saveErr) {
				result = saveErr.Err
			}
			result = &ArchiveSaveError{Err: errors.Join(result, fmt.Errorf("closing archive part: %w", err))}
		}
	}()
	if resumeFrom == 0 {
		if err := out.Truncate(0); err != nil {
			return &ArchiveSaveError{Err: fmt.Errorf("truncating archive part: %w", err)}
		}
	}

	buf := make([]byte, 64*1024)
	var lastLedger time.Time
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if expected >= 0 && int64(n) > expected-state.BytesDownloaded {
				return fmt.Errorf("%w: response exceeds expected total", ErrDownloadFailed)
			}
			for written := 0; written < n; {
				count, writeErr := out.Write(buf[written:n])
				if count > 0 {
					written += count
					state = m.update(dl, func(d *Download) { d.BytesDownloaded += int64(count) })
				}
				if writeErr != nil {
					return &ArchiveSaveError{Err: fmt.Errorf("writing archive part: %w", writeErr)}
				}
				if written < n {
					return &ArchiveSaveError{Err: fmt.Errorf("writing archive part: %w", io.ErrShortWrite)}
				}
			}
			m.emitProgress(state)
			if time.Since(lastLedger) > time.Second {
				lastLedger = time.Now()
				_ = UpsertLedgerEntry(LedgerEntry{
					ID: state.ID, GameID: state.GameID, NXMURI: state.NXMURI,
					GameSlug: state.GameSlug, ModID: state.ModID, FileID: state.FileID,
					ArchiveRelPath: state.ArchiveRel,
					BytesDone:      state.BytesDownloaded, BytesTotal: state.BytesTotal,
					Status: LedgerDownloading,
				})
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				if expected >= 0 && state.BytesDownloaded < expected {
					return fmt.Errorf("%w: incomplete: %w", ErrDownloadFailed, redactHTTPError(readErr))
				}
				return fmt.Errorf("%w: reading response: %w", ErrDownloadFailed, redactHTTPError(readErr))
			}
			if expected >= 0 && state.BytesDownloaded != expected {
				return fmt.Errorf("%w: incomplete", ErrDownloadFailed)
			}
			if err := out.Sync(); err != nil {
				return &ArchiveSaveError{Err: fmt.Errorf("syncing archive part: %w", err)}
			}
			return nil
		}
	}
}

// parseDownloadRange checks that a partial response covers the requested suffix.
func parseDownloadRange(value string, start int64) (int64, int64, error) {
	bounds, totalText, ok := strings.Cut(value, "/")
	if !ok || !strings.HasPrefix(bounds, "bytes ") {
		return 0, 0, fmt.Errorf("missing byte range")
	}
	first, last, ok := strings.Cut(strings.TrimPrefix(bounds, "bytes "), "-")
	if !ok || first == "" || last == "" || strings.Trim(first, "0123456789") != "" || strings.Trim(last, "0123456789") != "" {
		return 0, 0, fmt.Errorf("invalid byte bounds")
	}
	from, err := strconv.ParseInt(first, 10, 64)
	if err != nil || from != start {
		return 0, 0, fmt.Errorf("unexpected range start")
	}
	end, err := strconv.ParseInt(last, 10, 64)
	if err != nil || end < from {
		return 0, 0, fmt.Errorf("invalid range end")
	}
	if totalText == "*" {
		return 0, end, nil
	}
	if totalText == "" || strings.Trim(totalText, "0123456789") != "" {
		return 0, 0, fmt.Errorf("invalid range total")
	}
	total, err := strconv.ParseInt(totalText, 10, 64)
	if err != nil || total <= 0 || end != total-1 {
		return 0, 0, fmt.Errorf("invalid range total")
	}
	return total, end, nil
}

func (m *Manager) fail(dl *Download, err error) {
	state := m.update(dl, func(d *Download) {
		d.Status = StatusFailed
		d.Error = err.Error()
	})
	m.emitProgress(state)
	var saveErr *ArchiveSaveError
	if errors.As(err, &saveErr) {
		slog.Error("download failed", "id", state.ID, "err", redactHTTPError(saveErr.Err))
	} else {
		slog.Error("download failed", "id", state.ID, "err", err)
	}
	_ = UpsertLedgerEntry(LedgerEntry{
		ID: state.ID, GameID: state.GameID, NXMURI: state.NXMURI,
		GameSlug: state.GameSlug, ModID: state.ModID, FileID: state.FileID,
		ArchiveRelPath: state.ArchiveRel,
		BytesDone:      state.BytesDownloaded, BytesTotal: state.BytesTotal,
		Status: LedgerFailed, Error: err.Error(),
	})
}

// update changes a download under the manager lock and returns its immutable state.
func (m *Manager) update(dl *Download, change func(*Download)) Download {
	m.mu.Lock()
	change(dl)
	state := *dl
	m.mu.Unlock()
	return state
}

// snapshot copies a download under the manager lock.
func (m *Manager) snapshot(dl *Download) Download {
	m.mu.RLock()
	state := *dl
	m.mu.RUnlock()
	return state
}

// claimDestination reserves an archive for one pipeline.
func (m *Manager) claimDestination(gameID, rel, id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.destinations == nil {
		m.destinations = make(map[string]string)
	}
	key := gameID + "\x00" + rel
	if _, taken := m.destinations[key]; taken {
		return false
	}
	m.destinations[key] = id
	return true
}

// releaseDestination frees an archive after its pipeline finishes.
func (m *Manager) releaseDestination(gameID, rel, id string) {
	m.mu.Lock()
	key := gameID + "\x00" + rel
	if m.destinations[key] == id {
		delete(m.destinations, key)
	}
	m.mu.Unlock()
}

func (m *Manager) emitProgress(dl Download) {
	if m.hooks.OnDownloadProgress != nil {
		m.hooks.OnDownloadProgress(snapshotOf(&dl))
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
	if details != nil {
		if name, ok := SafeArchiveFilename(details.FileName); ok {
			return name
		}
	}
	if u, err := neturl.Parse(downloadURL); err == nil {
		if name, ok := SafeArchiveFilename(u.Path); ok {
			return name
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
