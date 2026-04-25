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

// Status aliases — the ipc-typed constants are the single source of truth.
const (
	StatusQueued      = ipc.DownloadStatusQueued
	StatusDownloading = ipc.DownloadStatusDownloading
	StatusDownloaded  = ipc.DownloadStatusDownloaded
	StatusInstalling  = ipc.DownloadStatusInstalling
	StatusInstalled   = ipc.DownloadStatusInstalled
	StatusCancelled   = ipc.DownloadStatusCancelled
	StatusFailed      = ipc.DownloadStatusFailed
)

// Download tracks a single download operation. Owned by the Manager; the
// daemon reads a snapshot via ProgressFor (so callers don't share mutexes).
type Download struct {
	ID              string
	GameID          string
	ModName         string
	NXMURI          string
	ModID           int
	FileID          int
	GameSlug        string
	ArchiveRel      string // relative to DownloadsDir(gameID)
	Status          ipc.DownloadStatus
	BytesDownloaded int64
	BytesTotal      int64
	Error           string
	QueuedAhead     int32

	cancel context.CancelFunc
}

// PostInstallHook is invoked after the auto-install path successfully
// installs a mod. Used by the daemon to wire a fresh mod into the profiles'
// modlist.txt so it's visible to the VFS.
type PostInstallHook func(gameID, modName string)

// Manager orchestrates the full download-extract-install pipeline.
//
// Responsibilities:
//   - Queue NXM requests (bounded concurrency).
//   - Drive each download through HTTP + optional Range resume.
//   - Write to <archive>.part and atomic-rename on success.
//   - Persist every state transition to the per-game ledger so a daemon
//     restart can resume or mark-as-failed cleanly.
//   - Emit progress via Hooks (the daemon fans these out to the per-game
//     stream).
type Manager struct {
	nexus       URLResolver
	config      *config.Config
	mu          sync.RWMutex
	active      map[string]*Download // id → download (running)
	queued      []*Download          // FIFO; cancel = remove
	hooks       ManagerHooks
	postInstall PostInstallHook
	maxConcur   int

	// queuePump is signaled whenever a slot frees or a new download is
	// enqueued; the pump goroutine promotes queued → active.
	queuePump chan struct{}
	stop      chan struct{}
	stopped   bool
	stopMu    sync.Mutex
}

// ManagerHooks are the streaming callbacks invoked as downloads progress.
// All hooks are called synchronously from the manager's goroutines; they
// MUST be non-blocking (typical impl: non-blocking send to a buffered
// channel).
type ManagerHooks struct {
	// OnDownloadProgress fires for every status transition + throttled byte
	// updates on active downloads.
	OnDownloadProgress func(snapshot DownloadSnapshot)
	// OnArchiveLanded fires when a download completes and the archive is
	// renamed into its final path. The daemon uses this to index the
	// archive and (optionally) auto-install.
	OnArchiveLanded func(d DownloadSnapshot, archivePath string, sidecar ArchiveSidecar)
}

// DownloadSnapshot is a lock-free copy of a Download for broadcasting to
// progress listeners.
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

// NewManager creates a download Manager. The caller is responsible for
// calling Stop() on shutdown. `maxConcurrent` bounds simultaneous
// in-flight downloads; queueing kicks in above that.
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

// SetPostInstallHook registers a callback invoked after a mod is
// auto-installed from a completed download.
func (m *Manager) SetPostInstallHook(hook PostInstallHook) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.postInstall = hook
}

// Stop halts the queue pump. Active downloads continue (the caller should
// cancel them individually first if immediate teardown is required).
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

// StartDownload enqueues a new download from an NXM URI. Returns the
// persistent UUID and the caller's position in the queue at enqueue time
// (0 means it started immediately).
func (m *Manager) StartDownload(uri string) (id string, queuedAhead int, err error) {
	link, err := ParseNXM(uri)
	if err != nil {
		return "", 0, err
	}
	gameID, err := link.GameID()
	if err != nil {
		return "", 0, err
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

	// Persist immediately — if the daemon dies between here and the pump
	// activating, a restart still sees the queued entry.
	_ = UpsertLedgerEntry(LedgerEntry{
		ID: id, NXMURI: uri, GameID: gameID, GameSlug: link.GameSlug,
		ModID: link.ModID, FileID: link.FileID,
		Status: LedgerQueued, StartedAt: time.Now(),
	})

	m.mu.Lock()
	m.queued = append(m.queued, dl)
	ahead := len(m.queued) + len(m.active) - 1 // rough position
	if ahead < 0 {
		ahead = 0
	}
	dl.QueuedAhead = int32(ahead)
	m.mu.Unlock()

	m.emitProgress(dl)
	m.signalPump()
	return id, ahead, nil
}

// RetryDownload restarts a failed / cancelled download. The ID must still
// be in the manager (active, queued, or ledger). Resolves a fresh URL and
// resumes from bytes_done if a .part file exists.
func (m *Manager) RetryDownload(id string) (queuedAhead int, err error) {
	// Try live first.
	m.mu.Lock()
	if dl, ok := m.active[id]; ok {
		_ = dl
		m.mu.Unlock()
		return 0, nil // already running — nothing to do
	}
	for _, dl := range m.queued {
		if dl.ID == id {
			m.mu.Unlock()
			return int(dl.QueuedAhead), nil
		}
	}
	m.mu.Unlock()

	// Fall back to the ledger. Any game ID works — we scan all games.
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
			// Update ledger status back to queued.
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
// Partial .part files are removed. Ledger is marked cancelled.
func (m *Manager) CancelDownload(id string) error {
	m.mu.Lock()
	if dl, ok := m.active[id]; ok {
		if dl.cancel != nil {
			dl.cancel()
		}
		m.mu.Unlock()
		return nil
	}
	// Queued?
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

	// Could be a terminal ledger entry; look it up and flip to cancelled.
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

// GetProgress returns a snapshot for one download, or nil if unknown.
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

// ActiveDownloadIDByArchive returns the live download ID whose on-disk
// archive path (absolute, .part or final) matches, or "" if none.
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

// ActiveDownloadIDByNXM looks up an in-flight download by NXM URI. Used on
// daemon startup resume to dedupe a ledger entry that's already active.
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

// RehydrateLedger scans each configured game's ledger and re-enqueues any
// non-terminal entries for resume. Called once on daemon startup.
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

// --- internal pump / pipeline ---

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
			// Recompute queued_ahead for everyone still waiting.
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
			// Update still-queued entries' positions so clients see the
			// promotion shift.
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

	// Fetch display metadata. Best-effort — continue even if it fails.
	modInfo, err := m.nexus.GetModInfo(link.GameSlug, link.ModID)
	modName := fmt.Sprintf("mod_%d", link.ModID)
	if err == nil && modInfo != nil {
		modName = modInfo.Name
	}
	dl.ModName = modName
	m.emitProgress(dl)

	fileDetails, _ := m.nexus.GetFileDetails(link.GameSlug, link.ModID, link.FileID)

	// Determine archive target. Preserve the ledger's pre-existing rel
	// path when resuming so we don't orphan a previously-started .part.
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

	// Resolve a fresh CDN URL every time — the previous one is single-use.
	cdnURL, err := m.nexus.ResolveDownloadURL(link)
	if err != nil {
		m.fail(dl, fmt.Errorf("resolving CDN URL: %w", err))
		return
	}

	// Determine resume offset from on-disk .part size.
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
			// Remove .part on cancel — user explicitly asked to abort.
			os.Remove(partPath)
			return
		}
		m.fail(dl, err)
		return
	}

	// Atomic rename .part → final.
	if err := os.Rename(partPath, archivePath); err != nil {
		m.fail(dl, fmt.Errorf("renaming .part: %w", err))
		return
	}

	// Build sidecar + upsert index.
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

	// Ledger: mark downloaded; the long-term state lives in the index +
	// sidecar from here on.
	_ = RemoveLedgerEntry(dl.GameID, dl.ID)

	dl.Status = StatusDownloaded
	m.emitProgress(dl)

	// Fire the archive-landed hook — daemon may index or auto-install.
	if m.hooks.OnArchiveLanded != nil {
		m.hooks.OnArchiveLanded(snapshotOf(dl), archivePath, sidecar)
	}

	slog.Info("download complete", "name", modName, "game", dl.GameID,
		"archive", archivePath, "bytes", dl.BytesDownloaded)
}

// streamToFile GETs cdnURL (with a Range header when resuming), writes
// bytes into destPath (append mode), throttles ledger writes, and pushes
// progress via the manager's hook.
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
		// Server ignored Range; start from 0.
		if resumeFrom > 0 {
			slog.Warn("server ignored Range header; restarting from 0", "url", cdnURL)
			resumeFrom = 0
			_ = os.Truncate(destPath, 0)
			dl.BytesDownloaded = 0
		}
	case http.StatusPartialContent:
		// Happy resume path.
	default:
		return fmt.Errorf("%w: HTTP %d", ErrDownloadFailed, resp.StatusCode)
	}

	// Content-Length on a Range response is the *remaining* bytes; add
	// resumeFrom to get the total.
	if cl := resp.ContentLength; cl > 0 {
		dl.BytesTotal = cl + resumeFrom
	}

	// Open destination append/write.
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

// pickArchiveFilename chooses the on-disk filename for an archive. Prefers
// the Nexus-reported file_name, falls back to the URL path tail, then to
// "{modID}_{fileID}.archive" as a last resort.
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

// IsExpired is true when the NXM URI's `expires` query has passed.
// Used on startup rehydrate and on StartDownload to fail fast.
func (l *NXMLink) IsExpired(now time.Time) bool {
	if l.Expires == 0 {
		return false
	}
	return now.Unix() >= l.Expires
}

// nexusModPageURL returns the canonical Nexus Mods URL for a mod, or "" if
// the inputs are incomplete.
func nexusModPageURL(gameDomain string, modID int) string {
	if gameDomain == "" || modID <= 0 {
		return ""
	}
	return fmt.Sprintf("https://www.nexusmods.com/%s/mods/%d", gameDomain, modID)
}
