package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/smapi"
)

const (
	dependencyRequestsFileName = ".gorganizer-dependency-requests.json"
	dependencyRequestsSchema   = 1
	dependencyRequestsMaxBytes = 4 << 20
	browserRequestTTL          = 24 * time.Hour
	finishedBatchRetention     = 7 * 24 * time.Hour
	dependencyBatchRetention   = 30 * 24 * time.Hour
	failedDownloadRetention    = 7 * 24 * time.Hour
	pendingDownloadGrace       = 2 * time.Minute
	recentFailureWindow        = 7 * 24 * time.Hour
	recentFailureLimit         = 50
	downloadOutcomeRetention   = time.Hour
	downloadOutcomeLimit       = 256
	landingReadRetryDelay      = 100 * time.Millisecond
	landingClockSlack          = 2 * time.Second

	depStateAwaitingDownload = "awaiting_download"
	depStateDownloading      = "downloading"
	depStateInstalling       = "installing"
	depStateEnablePending    = "enable_pending"
	depStateDone             = "done"
	depStateFailed           = "failed"
	depStateExpired          = "expired"

	detailArchiveMismatch    = "archive_mismatch"
	detailDownloadFailed     = "download_failed"
	detailDownloadLost       = "download_lost"
	detailInstallFailed      = "install_failed"
	detailInstallInterrupted = "install_interrupted"
	detailNotInModlist       = "not_in_modlist"
)

type depRequestsDoc struct {
	SchemaVersion   int                 `json:"schema_version"`
	Batches         []depBatch          `json:"batches"`
	FailedDownloads []depFailedDownload `json:"failed_downloads,omitempty"`
}

type depBatch struct {
	BatchID   string     `json:"batch_id"`
	Profile   string     `json:"profile"`
	CreatedAt time.Time  `json:"created_at"`
	Entries   []depEntry `json:"entries"`
}

type depEntry struct {
	UniqueID   string     `json:"unique_id"`
	NexusModID int        `json:"nexus_mod_id"`
	FileID     int        `json:"file_id"`
	DownloadID string     `json:"download_id"`
	State      string     `json:"state"`
	ModName    string     `json:"mod_name"`
	ExpiresAt  *time.Time `json:"expires_at"`
	Detail     string     `json:"detail"`
	Install    string     `json:"install,omitempty"`
	Archive    string     `json:"archive,omitempty"`
	UpdatedAt  *time.Time `json:"updated_at,omitempty"`
}

type depFailedDownload struct {
	DownloadID string    `json:"download_id"`
	Detail     string    `json:"detail"`
	At         time.Time `json:"at"`
}

type depEntryRef struct {
	batchID string
	idKey   string
}

type depRequestLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

type downloadOutcome struct {
	landed bool
	detail string
	at     time.Time
}

type downloadOutcomes struct {
	mu    sync.Mutex
	games map[string]map[string]downloadOutcome
}

type ledgerSnapshot struct {
	gameID  string
	loaded  bool
	entries []download.LedgerEntry
}

type archiveCheck struct {
	root    string
	planned map[string]bool
	notAMod error
}

type recoveredLanding struct {
	gameID     string
	downloadID string
	install    string
	path       string
	sidecar    download.ArchiveSidecar
}

// dependencyRequestsPath returns the durable dependency request file of gameID.
func dependencyRequestsPath(gameID string) string {
	return filepath.Join(config.ModsDir(gameID), dependencyRequestsFileName)
}

// lock returns gameID's leaf mutex guarding its dependency request file.
func (l *depRequestLocks) lock(gameID string) *sync.Mutex {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.locks == nil {
		l.locks = make(map[string]*sync.Mutex)
	}
	m, ok := l.locks[gameID]
	if !ok {
		m = &sync.Mutex{}
		l.locks[gameID] = m
	}
	return m
}

// updateRequests loads gameID's requests under its leaf lock, expires stale browser entries, applies mutate and saves when anything changed.
func (md *ModDependencyService) updateRequests(gameID string, mutate func(doc *depRequestsDoc, now time.Time) bool) error {
	lock := md.requestLocks.lock(gameID)
	lock.Lock()
	defer lock.Unlock()
	now := md.s.clock()
	load := loadDependencyRequests
	if md.loadRequests != nil {
		load = md.loadRequests
	}
	doc, err := load(gameID)
	if err != nil {
		return err
	}
	changed := expireDependencyRequests(doc, now)
	if mutate != nil && mutate(doc, now) {
		changed = true
	}
	if !changed {
		return nil
	}
	pruneDependencyRequests(doc, now)
	return saveDependencyRequests(gameID, doc)
}

// loadDependencyRequests reads gameID's request file, starting empty when it is missing and moving a corrupt file aside.
func loadDependencyRequests(gameID string) (*depRequestsDoc, error) {
	path := dependencyRequestsPath(gameID)
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &depRequestsDoc{SchemaVersion: dependencyRequestsSchema}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading dependency requests: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(file, dependencyRequestsMaxBytes+1))
	file.Close()
	if err != nil {
		return nil, fmt.Errorf("reading dependency requests: %w", err)
	}
	var doc depRequestsDoc
	if len(data) > dependencyRequestsMaxBytes || json.Unmarshal(data, &doc) != nil {
		aside := path + ".corrupt"
		if renameErr := os.Rename(path, aside); renameErr != nil {
			return nil, fmt.Errorf("moving corrupt dependency requests aside: %w", renameErr)
		}
		slog.Warn("dependency request file was corrupt; moved aside", "game", gameID, "path", aside)
		return &depRequestsDoc{SchemaVersion: dependencyRequestsSchema}, nil
	}
	if doc.SchemaVersion != dependencyRequestsSchema {
		return nil, fmt.Errorf("dependency requests %s: unsupported schema_version %d", path, doc.SchemaVersion)
	}
	return &doc, nil
}

// saveDependencyRequests writes gameID's request file atomically.
func saveDependencyRequests(gameID string, doc *depRequestsDoc) error {
	doc.SchemaVersion = dependencyRequestsSchema
	if doc.Batches == nil {
		doc.Batches = []depBatch{}
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding dependency requests: %w", err)
	}
	if err := os.MkdirAll(config.ModsDir(gameID), 0o755); err != nil {
		return fmt.Errorf("creating mods directory: %w", err)
	}
	if err := atomicfile.WriteFile(dependencyRequestsPath(gameID), append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing dependency requests: %w", err)
	}
	return nil
}

// expireDependencyRequests marks browser entries past their deadline expired and reports whether any changed.
func expireDependencyRequests(doc *depRequestsDoc, now time.Time) bool {
	changed := false
	for bi := range doc.Batches {
		for ei := range doc.Batches[bi].Entries {
			entry := &doc.Batches[bi].Entries[ei]
			if entry.State == depStateAwaitingDownload && entry.expired(now) {
				entry.set(depStateExpired, entry.Detail, now)
				changed = true
			}
		}
	}
	return changed
}

// pruneDependencyRequests drops batches whose entries are all finished, batches past the retention window and old download failures.
func pruneDependencyRequests(doc *depRequestsDoc, now time.Time) {
	kept := doc.Batches[:0]
	for _, batch := range doc.Batches {
		old := now.Sub(batch.CreatedAt) > dependencyBatchRetention
		if old || batch.finished() && now.Sub(batch.CreatedAt) > finishedBatchRetention {
			continue
		}
		kept = append(kept, batch)
	}
	doc.Batches = kept
	failures := doc.FailedDownloads[:0]
	for _, failure := range doc.FailedDownloads {
		if now.Sub(failure.At) <= failedDownloadRetention {
			failures = append(failures, failure)
		}
	}
	doc.FailedDownloads = failures
}

// finished reports whether every entry of the batch reached a terminal state.
func (b depBatch) finished() bool {
	for _, entry := range b.Entries {
		switch entry.State {
		case depStateDone, depStateFailed, depStateExpired:
		default:
			return false
		}
	}
	return true
}

// expired reports whether a browser entry's deadline has passed at now.
func (e depEntry) expired(now time.Time) bool {
	return e.ExpiresAt != nil && !now.Before(*e.ExpiresAt)
}

// set moves the entry to state with detail, stamping the change time.
func (e *depEntry) set(state, detail string, now time.Time) {
	stamp := now.UTC()
	e.State = state
	e.Detail = detail
	e.UpdatedAt = &stamp
}

// changedAt returns when the entry last changed, falling back to its batch's creation time.
func (e depEntry) changedAt(batch depBatch) time.Time {
	if e.UpdatedAt != nil {
		return *e.UpdatedAt
	}
	return batch.CreatedAt
}

// entry returns the entry ref names, or nil when it no longer exists.
func (doc *depRequestsDoc) entry(ref depEntryRef) *depEntry {
	for bi := range doc.Batches {
		if doc.Batches[bi].BatchID != ref.batchID {
			continue
		}
		for ei := range doc.Batches[bi].Entries {
			if idKey(doc.Batches[bi].Entries[ei].UniqueID) == ref.idKey {
				return &doc.Batches[bi].Entries[ei]
			}
		}
	}
	return nil
}

// each calls visit for every entry with its batch.
func (doc *depRequestsDoc) each(visit func(batch *depBatch, entry *depEntry)) {
	for bi := range doc.Batches {
		for ei := range doc.Batches[bi].Entries {
			visit(&doc.Batches[bi], &doc.Batches[bi].Entries[ei])
		}
	}
}

// failedDownload returns the recorded failure detail of downloadID.
func (doc *depRequestsDoc) failedDownload(downloadID string) (string, bool) {
	for _, failure := range doc.FailedDownloads {
		if failure.DownloadID == downloadID {
			return failure.Detail, true
		}
	}
	return "", false
}

// recordFailedDownload remembers that downloadID failed with detail.
func (doc *depRequestsDoc) recordFailedDownload(downloadID, detail string, now time.Time) {
	for i := range doc.FailedDownloads {
		if doc.FailedDownloads[i].DownloadID == downloadID {
			doc.FailedDownloads[i] = depFailedDownload{DownloadID: downloadID, Detail: detail, At: now.UTC()}
			return
		}
	}
	doc.FailedDownloads = append(doc.FailedDownloads, depFailedDownload{DownloadID: downloadID, Detail: detail, At: now.UTC()})
}

// forgetFailedDownload drops the failure record of downloadID and reports whether one existed.
func (doc *depRequestsDoc) forgetFailedDownload(downloadID string) bool {
	for i := range doc.FailedDownloads {
		if doc.FailedDownloads[i].DownloadID == downloadID {
			doc.FailedDownloads = append(doc.FailedDownloads[:i], doc.FailedDownloads[i+1:]...)
			return true
		}
	}
	return false
}

// awaitsDownloadID reports whether a premium entry is still waiting for its download ID to be recorded.
func (doc *depRequestsDoc) awaitsDownloadID() bool {
	waiting := false
	doc.each(func(_ *depBatch, entry *depEntry) {
		if entry.State == depStateDownloading && entry.DownloadID == "" {
			waiting = true
		}
	})
	return waiting
}

// failDownloadEntries fails every premium entry waiting on downloadID with detail.
func (doc *depRequestsDoc) failDownloadEntries(downloadID, detail string, now time.Time) bool {
	changed := false
	doc.each(func(_ *depBatch, entry *depEntry) {
		if entry.State == depStateDownloading && entry.DownloadID == downloadID {
			entry.set(depStateFailed, detail, now)
			changed = true
		}
	})
	return changed
}

// landedMatch reports whether entry waits for the archive of downloadID that landed with sidecar at now.
func landedMatch(entry depEntry, downloadID string, sidecar download.ArchiveSidecar, now time.Time) bool {
	switch entry.State {
	case depStateDownloading:
		if entry.FileID <= 0 || entry.FileID != sidecar.FileID {
			return false
		}
		return downloadID != "" && entry.DownloadID == downloadID || entry.NexusModID > 0 && entry.NexusModID == sidecar.ModID
	case depStateAwaitingDownload:
		return entry.NexusModID > 0 && entry.NexusModID == sidecar.ModID && !entry.expired(now)
	case depStateFailed:
		if !strings.HasPrefix(entry.Detail, detailDownloadFailed) || entry.FileID <= 0 || entry.FileID != sidecar.FileID {
			return false
		}
		return downloadID != "" && entry.DownloadID == downloadID
	}
	return false
}

// idKey returns the case-folded, trimmed comparison key of a SMAPI UniqueID.
func idKey(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

// note records the terminal outcome of a download in memory so later fetches never attach to it.
func (o *downloadOutcomes) note(gameID, downloadID string, outcome downloadOutcome) {
	if downloadID == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.games == nil {
		o.games = map[string]map[string]downloadOutcome{}
	}
	game := o.games[gameID]
	if game == nil {
		game = map[string]downloadOutcome{}
		o.games[gameID] = game
	}
	game[downloadID] = outcome
	if len(game) <= downloadOutcomeLimit {
		return
	}
	for id, old := range game {
		if outcome.at.Sub(old.at) > downloadOutcomeRetention {
			delete(game, id)
		}
	}
	for id := range game {
		if len(game) <= downloadOutcomeLimit {
			break
		}
		if id != downloadID {
			delete(game, id)
		}
	}
}

// lookup returns the remembered outcome of a download.
func (o *downloadOutcomes) lookup(gameID, downloadID string) (downloadOutcome, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	outcome, ok := o.games[gameID][downloadID]
	return outcome, ok
}

// clearFailure forgets a remembered failure of a download the manager queued again.
func (o *downloadOutcomes) clearFailure(gameID, downloadID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if outcome, ok := o.games[gameID][downloadID]; ok && !outcome.landed {
		delete(o.games[gameID], downloadID)
	}
}

// nonTerminal reports whether the download ledger holds a pending entry for downloadID, loading the ledger once.
func (l *ledgerSnapshot) nonTerminal(downloadID string) bool {
	l.load()
	for _, entry := range l.entries {
		if entry.ID == downloadID && entry.GameID == l.gameID && !entry.Terminal() {
			return true
		}
	}
	return false
}

// pendingFor returns the ID of a pending ledger download of the Nexus file, or "".
func (l *ledgerSnapshot) pendingFor(modID, fileID int) string {
	l.load()
	for _, entry := range l.entries {
		if entry.GameID == l.gameID && !entry.Terminal() && modID > 0 && entry.ModID == modID && fileID > 0 && entry.FileID == fileID {
			return entry.ID
		}
	}
	return ""
}

// failure returns the recorded error of a failed or cancelled ledger download matching the entry's ID or Nexus file.
func (l *ledgerSnapshot) failure(entry depEntry) (string, bool) {
	l.load()
	for _, ledger := range l.entries {
		if ledger.GameID != l.gameID || ledger.Status != download.LedgerFailed && ledger.Status != download.LedgerCancelled {
			continue
		}
		byID := entry.DownloadID != "" && ledger.ID == entry.DownloadID
		byFile := entry.DownloadID == "" && entry.NexusModID > 0 && ledger.ModID == entry.NexusModID && entry.FileID > 0 && ledger.FileID == entry.FileID
		if byID || byFile {
			reason := ledger.Error
			if reason == "" {
				reason = string(ledger.Status)
			}
			return reason, true
		}
	}
	return "", false
}

// load reads the ledger on first use, treating a read failure as empty.
func (l *ledgerSnapshot) load() {
	if l.loaded {
		return
	}
	l.loaded = true
	entries, err := download.LoadLedger(l.gameID)
	if err != nil {
		slog.Warn("reading the download ledger for dependency requests failed", "game", l.gameID, "err", err)
		return
	}
	l.entries = entries
}

// downloadLive reports whether downloadID is still queued or running, never for a download already known to have landed or failed.
func (md *ModDependencyService) downloadLive(gameID, downloadID string, downloader dependencyDownloader, ledger *ledgerSnapshot) bool {
	if downloadID == "" {
		return false
	}
	if _, finished := md.outcomes.lookup(gameID, downloadID); finished {
		return false
	}
	if downloader != nil {
		if _, err := downloader.GetProgress(downloadID); err == nil {
			return true
		}
	}
	return ledger.nonTerminal(downloadID)
}

// observeDownload remembers failed downloads for later fetches and fails the premium requests waiting on them.
func (md *ModDependencyService) observeDownload(snap download.DownloadSnapshot) {
	switch snap.Status {
	case download.StatusFailed, download.StatusCancelled:
		if _, _, err := dependencySpecFor(snap.GameID); err != nil {
			return
		}
		reason := snap.Error
		if reason == "" {
			reason = "download failed"
		}
		md.outcomes.note(snap.GameID, snap.ID, downloadOutcome{detail: detailDownloadFailed + ": " + reason, at: time.Now()})
		go md.failDownload(snap.GameID, snap.ID, reason)
	case download.StatusQueued, download.StatusDownloading:
		md.outcomes.clearFailure(snap.GameID, snap.ID)
	}
}

// consumeLandedArchive installs a landed archive for the dependency requests it satisfies and reports whether it handled the archive.
func (md *ModDependencyService) consumeLandedArchive(gameID, downloadID, archivePath string, sidecar download.ArchiveSidecar) bool {
	if _, _, err := dependencySpecFor(gameID); err != nil {
		return false
	}
	if slug := nexusSlug(gameID); sidecar.GameDomain != "" && !strings.EqualFold(sidecar.GameDomain, slug) {
		return false
	}
	md.outcomes.note(gameID, downloadID, downloadOutcome{landed: true, at: time.Now()})
	candidates, err := md.landingCandidates(gameID, downloadID, sidecar)
	if err != nil {
		slog.Error("reading dependency requests for a landed archive failed; startup reconciliation will retry", "game", gameID, "download", downloadID, "archive", archivePath, "err", err)
		return false
	}
	if len(candidates) == 0 {
		return false
	}
	check, checkErr := md.checkLandedArchive(archivePath)
	if checkErr != nil {
		slog.Warn("checking a landed dependency archive failed; its requests keep waiting", "game", gameID, "archive", archivePath, "err", checkErr)
		return true
	}
	install := uuid.NewString()
	rel := relFromDownloads(gameID, archivePath)
	consumed := 0
	err = md.updateRequests(gameID, func(doc *depRequestsDoc, now time.Time) bool {
		changed := false
		for _, ref := range candidates {
			entry := doc.entry(ref)
			if entry == nil || !landedMatch(*entry, downloadID, sidecar, now) {
				continue
			}
			if check.planned[ref.idKey] {
				entry.Install = install
				entry.Archive = rel
				entry.set(depStateInstalling, "", now)
				consumed++
				changed = true
				continue
			}
			if entry.State != depStateAwaitingDownload {
				entry.set(depStateFailed, archiveMismatchDetail(entry.UniqueID, check.notAMod), now)
				changed = true
			}
		}
		if downloadID != "" && doc.forgetFailedDownload(downloadID) {
			changed = true
		}
		return changed
	})
	if err != nil {
		md.dropCheck(check.root)
		slog.Warn("recording consumed dependency requests failed; they keep waiting", "game", gameID, "err", err)
		return true
	}
	if consumed == 0 {
		md.dropCheck(check.root)
		return false
	}
	md.installForRequests(gameID, rel, check.root, install, check.planned)
	return true
}

// landingCandidates lists the entries a landing may satisfy, retrying one failed read of the request file.
func (md *ModDependencyService) landingCandidates(gameID, downloadID string, sidecar download.ArchiveSidecar) ([]depEntryRef, error) {
	var candidates []depEntryRef
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			slog.Warn("reading dependency requests for a landed archive failed; retrying", "game", gameID, "err", err)
			time.Sleep(landingReadRetryDelay)
		}
		candidates = nil
		err = md.updateRequests(gameID, func(doc *depRequestsDoc, now time.Time) bool {
			doc.each(func(batch *depBatch, entry *depEntry) {
				if landedMatch(*entry, downloadID, sidecar, now) {
					candidates = append(candidates, depEntryRef{batchID: batch.BatchID, idKey: idKey(entry.UniqueID)})
				}
			})
			return false
		})
		if err == nil {
			return candidates, nil
		}
	}
	return nil, err
}

// checkLandedArchive extracts an archive once into the daemon's extraction root and plans it, returning the extraction with its planned UniqueIDs or a not-a-mod verdict, and an error only for I/O failures.
func (md *ModDependencyService) checkLandedArchive(archivePath string) (archiveCheck, error) {
	root, err := extractArchive(archivePath)
	if err != nil {
		return archiveCheck{}, err
	}
	plan, err := smapi.PlanArchive(root)
	if err != nil && !errors.Is(err, smapi.ErrNotAMod) {
		md.dropCheck(root)
		return archiveCheck{}, err
	}
	check := archiveCheck{root: root, planned: make(map[string]bool, len(plan.Folders)), notAMod: err}
	for _, folder := range plan.Folders {
		if key := idKey(folder.UniqueID); key != "" {
			check.planned[key] = true
		}
	}
	return check, nil
}

// installForRequests installs a checked extraction as a new disabled mod, records the outcome on every entry attached to install, and announces the install with the batches whose update was recorded; an install refused or failed during shutdown leaves its entries installing for startup recovery.
func (md *ModDependencyService) installForRequests(gameID, rel, root, install string, planned map[string]bool) {
	defer md.dropCheck(root)
	folder, _, installErr := md.s.svc.install.startInstallFrom(dto.StartInstallRequest{
		GameID: gameID, ArchiveRelPath: rel, Mode: dto.InstallAsNewMod,
	}, root)
	if installErr != nil {
		if md.s.shuttingDown.Load() {
			slog.Warn("dependency install interrupted by shutdown; startup recovery resumes it", "game", gameID, "archive", rel, "err", installErr)
			return
		}
		slog.Warn("dependency install failed", "game", gameID, "archive", rel, "err", installErr)
	}
	var batchIDs []string
	err := md.updateRequests(gameID, func(doc *depRequestsDoc, now time.Time) bool {
		changed := false
		doc.each(func(batch *depBatch, entry *depEntry) {
			if entry.State != depStateInstalling || entry.Install != install {
				return
			}
			changed = true
			switch {
			case installErr != nil:
				entry.set(depStateFailed, detailInstallFailed+": "+installErr.Error(), now)
			case !planned[idKey(entry.UniqueID)]:
				entry.set(depStateFailed, archiveMismatchDetail(entry.UniqueID, nil), now)
			default:
				entry.ModName = folder
				entry.set(depStateEnablePending, "", now)
				batchIDs = appendUnique(batchIDs, batch.BatchID)
			}
		})
		return changed
	})
	if err != nil {
		slog.Warn("recording dependency install outcome failed; announcing the install without its dependency batches", "game", gameID, "err", err)
		batchIDs = nil
	}
	if installErr == nil {
		md.s.svc.install.publishInstallCompleted(gameID, folder, rel, batchIDs)
	}
}

// appendUnique appends value unless values already holds it.
func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

// archiveMismatchDetail explains why a matched premium download cannot satisfy uniqueID.
func archiveMismatchDetail(uniqueID string, planErr error) string {
	if planErr != nil {
		return detailArchiveMismatch + ": " + planErr.Error()
	}
	return detailArchiveMismatch + ": the archive does not contain " + uniqueID
}

// failDownload records a failed download and fails the premium requests waiting on it, even when its ID is recorded later.
func (md *ModDependencyService) failDownload(gameID, downloadID, reason string) {
	if downloadID == "" {
		return
	}
	if _, _, err := dependencySpecFor(gameID); err != nil {
		return
	}
	if reason == "" {
		reason = "download failed"
	}
	detail := detailDownloadFailed + ": " + reason
	err := md.updateRequests(gameID, func(doc *depRequestsDoc, now time.Time) bool {
		changed := doc.failDownloadEntries(downloadID, detail, now)
		if doc.awaitsDownloadID() {
			doc.recordFailedDownload(downloadID, detail, now)
			changed = true
		}
		return changed
	})
	if err != nil {
		slog.Warn("recording a failed dependency download failed", "game", gameID, "err", err)
	}
}

// recoverInterruptedRequests requeues interrupted installs and settles premium requests whose download is no longer pending, returning landed archives left to consume or adopt.
func (md *ModDependencyService) recoverInterruptedRequests(gameIDs []string) []recoveredLanding {
	sort.Strings(gameIDs)
	var landings []recoveredLanding
	for _, gameID := range gameIDs {
		if _, _, err := dependencySpecFor(gameID); err != nil {
			continue
		}
		if _, err := os.Lstat(dependencyRequestsPath(gameID)); err != nil {
			continue
		}
		ledger := &ledgerSnapshot{gameID: gameID}
		index, indexErr := download.LoadIndex(gameID)
		if indexErr != nil {
			slog.Warn("reading the downloads index for dependency recovery failed", "game", gameID, "err", indexErr)
			index = &download.DownloadsIndex{}
		}
		seen := map[string]bool{}
		installed := md.s.installedArchiveMap(gameID)
		err := md.updateRequests(gameID, func(doc *depRequestsDoc, now time.Time) bool {
			changed := false
			consumed := doc.consumedArchives()
			doc.each(func(batch *depBatch, entry *depEntry) {
				switch entry.State {
				case depStateInstalling:
					recoverInstallingEntry(gameID, entry, index, now, seen, &landings)
					changed = true
				case depStateDownloading:
					if recoverDownloadingEntry(gameID, entry, ledger, index, now, seen, &landings) {
						changed = true
					}
				case depStateAwaitingDownload:
					if !entry.expired(now) {
						queueBrowserLandings(gameID, *entry, entry.changedAt(*batch), index, installed, consumed, seen, &landings)
					}
				}
			})
			return changed
		})
		if err != nil {
			slog.Warn("recovering dependency requests failed", "game", gameID, "err", err)
		}
	}
	return landings
}

// recoverDownloadingEntry keeps a premium entry whose download is still pending, queues its landed archive for consumption, or fails it, reporting whether it changed.
func recoverDownloadingEntry(gameID string, entry *depEntry, ledger *ledgerSnapshot, index *download.DownloadsIndex, now time.Time, seen map[string]bool, landings *[]recoveredLanding) bool {
	if entry.DownloadID != "" && ledger.nonTerminal(entry.DownloadID) {
		return false
	}
	if id := ledger.pendingFor(entry.NexusModID, entry.FileID); id != "" {
		entry.DownloadID = id
		return true
	}
	if landing, ok := landedArchiveFor(gameID, index, entry.NexusModID, entry.FileID); ok {
		if !seen[landing.path] {
			seen[landing.path] = true
			landing.downloadID = entry.DownloadID
			*landings = append(*landings, landing)
		}
		return false
	}
	if reason, failed := ledger.failure(*entry); failed {
		entry.set(depStateFailed, detailDownloadFailed+": "+reason, now)
		return true
	}
	entry.set(depStateFailed, detailDownloadLost+": the download is no longer queued and no archive landed", now)
	return true
}

// recoverInstallingEntry queues the resumption of an install the previous daemon did not finish from the archive it consumed, or from the newest indexed archive of its Nexus file, keeping the entry installing, or fails it as interrupted when neither exists.
func recoverInstallingEntry(gameID string, entry *depEntry, index *download.DownloadsIndex, now time.Time, seen map[string]bool, landings *[]recoveredLanding) {
	landing, ok := consumedArchiveLanding(gameID, *entry)
	if !ok {
		landing, ok = landedArchiveFor(gameID, index, entry.NexusModID, entry.FileID)
	}
	if !ok {
		entry.set(depStateFailed, detailInstallInterrupted+": the daemon stopped during the install", now)
		return
	}
	if entry.Install == "" {
		entry.Install = uuid.NewString()
	}
	stamp := now.UTC()
	entry.UpdatedAt = &stamp
	key := landing.path + "\x00" + entry.Install
	if seen[key] {
		return
	}
	seen[key] = true
	landing.downloadID = entry.DownloadID
	landing.install = entry.Install
	*landings = append(*landings, landing)
}

// consumedArchiveLanding returns the archive an installing entry recorded at consumption when it still exists in the game's downloads folder.
func consumedArchiveLanding(gameID string, entry depEntry) (recoveredLanding, bool) {
	if entry.Archive == "" {
		return recoveredLanding{}, false
	}
	abs, err := archivePath(config.DownloadsDir(gameID), entry.Archive)
	if err != nil {
		return recoveredLanding{}, false
	}
	if info, statErr := os.Stat(abs); statErr != nil || !info.Mode().IsRegular() {
		return recoveredLanding{}, false
	}
	sidecar := download.ArchiveSidecar{ModID: entry.NexusModID, FileID: entry.FileID}
	if loaded, loadErr := download.LoadSidecar(abs); loadErr == nil && loaded != nil {
		sidecar = *loaded
	}
	return recoveredLanding{gameID: gameID, path: abs, sidecar: sidecar}, true
}

// consumedArchives returns the archives recorded by every entry that consumed one.
func (doc *depRequestsDoc) consumedArchives() map[string]bool {
	consumed := map[string]bool{}
	doc.each(func(_ *depBatch, entry *depEntry) {
		if entry.Archive != "" {
			consumed[entry.Archive] = true
		}
	})
	return consumed
}

// queueBrowserLandings queues for consumption every indexed archive of a browser entry's Nexus mod that landed after the entry was requested and was neither installed nor consumed, so a landing the previous daemon never handled still satisfies it.
func queueBrowserLandings(gameID string, entry depEntry, requestedAt time.Time, index *download.DownloadsIndex, installed map[string]archiveInstall, consumed, seen map[string]bool, landings *[]recoveredLanding) {
	if entry.NexusModID <= 0 {
		return
	}
	downloadsDir := config.DownloadsDir(gameID)
	for _, archive := range index.Archives {
		if archive.ModID != entry.NexusModID || consumed[archive.Path] || installed[filepath.Join("Downloads", archive.Path)].Folder != "" {
			continue
		}
		abs, err := archivePath(downloadsDir, archive.Path)
		if err != nil || seen[abs] {
			continue
		}
		info, statErr := os.Stat(abs)
		if statErr != nil || !info.Mode().IsRegular() || info.ModTime().Before(requestedAt.Add(-landingClockSlack)) {
			continue
		}
		sidecar := download.ArchiveSidecar{ModID: archive.ModID, FileID: archive.FileID}
		if loaded, loadErr := download.LoadSidecar(abs); loadErr == nil && loaded != nil {
			sidecar = *loaded
			sidecar.ModID, sidecar.FileID = archive.ModID, archive.FileID
		}
		seen[abs] = true
		*landings = append(*landings, recoveredLanding{gameID: gameID, path: abs, sidecar: sidecar})
	}
}

// landedArchiveFor returns the newest indexed archive of a Nexus file that exists on disk.
func landedArchiveFor(gameID string, index *download.DownloadsIndex, modID, fileID int) (recoveredLanding, bool) {
	if modID <= 0 || fileID <= 0 {
		return recoveredLanding{}, false
	}
	downloadsDir := config.DownloadsDir(gameID)
	for i := len(index.Archives) - 1; i >= 0; i-- {
		archive := index.Archives[i]
		if archive.ModID != modID || archive.FileID != fileID {
			continue
		}
		abs, err := archivePath(downloadsDir, archive.Path)
		if err != nil {
			continue
		}
		if info, statErr := os.Stat(abs); statErr != nil || !info.Mode().IsRegular() {
			continue
		}
		sidecar := download.ArchiveSidecar{ModID: modID, FileID: fileID}
		if loaded, loadErr := download.LoadSidecar(abs); loadErr == nil && loaded != nil {
			sidecar = *loaded
			sidecar.ModID, sidecar.FileID = modID, fileID
		}
		return recoveredLanding{gameID: gameID, path: abs, sidecar: sidecar}, true
	}
	return recoveredLanding{}, false
}

// resumeRecoveredLandings consumes, resumes or adopts, in tracked background work that waits for startup recovery, archives that landed for dependency requests or interrupted installs before the previous daemon stopped.
func (md *ModDependencyService) resumeRecoveredLandings(landings []recoveredLanding) {
	if len(landings) == 0 {
		return
	}
	md.s.goBackground("resume dependency landings", func() {
		if err := md.s.awaitRecovery(); err != nil {
			slog.Warn("resuming dependency landings skipped; the next start retries them", "err", err)
			return
		}
		for _, landing := range landings {
			rel := relFromDownloads(landing.gameID, landing.path)
			if installed := md.s.installedArchiveMap(landing.gameID)[filepath.Join("Downloads", rel)]; installed.Folder != "" {
				md.adoptInstalledArchive(landing, rel, installed.Folder)
				continue
			}
			if landing.install != "" {
				md.resumeInterruptedInstall(landing, rel)
				continue
			}
			md.consumeLandedArchive(landing.gameID, landing.downloadID, landing.path, landing.sidecar)
		}
	})
}

// resumeInterruptedInstall checks an interrupted install's archive again and installs it for every entry still attached to the install, leaving the entries installing when the check fails.
func (md *ModDependencyService) resumeInterruptedInstall(landing recoveredLanding, rel string) {
	check, err := md.checkLandedArchive(landing.path)
	if err != nil {
		slog.Warn("resuming an interrupted dependency install failed; the next start retries it", "game", landing.gameID, "archive", landing.path, "err", err)
		return
	}
	md.installForRequests(landing.gameID, rel, check.root, landing.install, check.planned)
}

// adoptInstalledArchive settles the requests of an archive already installed as modName by the UniqueIDs that mod provides: the entries of an interrupted install, or premium entries waiting on the archive, after listing the mod in each adopting entry's profile.
func (md *ModDependencyService) adoptInstalledArchive(landing recoveredLanding, rel, modName string) {
	gameID := landing.gameID
	provided := map[string]bool{}
	layer := projectionLayer{root: filepath.Join(config.ModsDir(gameID), modName), provider: modName, mod: true}
	for _, provider := range scanLayerProviders(gameID, layer, &projectionMeta{}) {
		if provider.Manifest != nil {
			provided[idKey(provider.Manifest.UniqueID)] = true
		}
	}
	adopts := func(entry depEntry, now time.Time) bool {
		if landing.install != "" {
			return entry.State == depStateInstalling && entry.Install == landing.install
		}
		return entry.State == depStateDownloading && landedMatch(entry, landing.downloadID, landing.sidecar, now)
	}
	var profiles []string
	err := md.updateRequests(gameID, func(doc *depRequestsDoc, now time.Time) bool {
		doc.each(func(batch *depBatch, entry *depEntry) {
			if adopts(*entry, now) && provided[idKey(entry.UniqueID)] {
				profiles = append(profiles, batch.Profile)
			}
		})
		return false
	})
	if err != nil {
		slog.Warn("adopting an installed dependency archive failed", "game", gameID, "err", err)
		return
	}
	listed := map[string]bool{}
	for _, profileName := range uniqueSorted(profiles) {
		if err := md.s.svc.mods.appendToProfileModList(gameID, profileName, modName); err != nil {
			slog.Warn("adding an adopted dependency to its profile's mod list failed; its request keeps waiting", "game", gameID, "profile", profileName, "mod", modName, "err", err)
			continue
		}
		listed[profileName] = true
	}
	var batchIDs []string
	err = md.updateRequests(gameID, func(doc *depRequestsDoc, now time.Time) bool {
		changed := false
		doc.each(func(batch *depBatch, entry *depEntry) {
			if !adopts(*entry, now) {
				return
			}
			if !provided[idKey(entry.UniqueID)] {
				entry.set(depStateFailed, archiveMismatchDetail(entry.UniqueID, nil), now)
				changed = true
				return
			}
			if !listed[batch.Profile] {
				return
			}
			entry.ModName = modName
			entry.set(depStateEnablePending, "", now)
			batchIDs = appendUnique(batchIDs, batch.BatchID)
			changed = true
		})
		return changed
	})
	if err != nil {
		slog.Warn("adopting an installed dependency archive failed", "game", gameID, "err", err)
		return
	}
	if len(batchIDs) > 0 {
		md.s.svc.install.publishInstallCompleted(gameID, modName, rel, batchIDs)
	}
}

// requestSummary lists profileName's pending dependency enables and its recent failed or expired requests, newest first, failing pending enables whose mod left the profile's modlist as not_in_modlist.
func (md *ModDependencyService) requestSummary(gameID, profileName string) ([]dto.PendingEnableResult, []dto.DependencyRequestIssueResult) {
	var pending []dto.PendingEnableResult
	var failures []dto.DependencyRequestIssueResult
	err := md.updateRequests(gameID, func(doc *depRequestsDoc, now time.Time) bool {
		changed := md.failEnablesOutsideModlist(doc, gameID, profileName, now)
		for _, batch := range doc.Batches {
			if batch.Profile != profileName {
				continue
			}
			for _, entry := range batch.Entries {
				switch entry.State {
				case depStateEnablePending:
					if entry.ModName != "" {
						pending = append(pending, dto.PendingEnableResult{
							BatchID: batch.BatchID, ProfileName: batch.Profile, ModName: entry.ModName, UniqueID: entry.UniqueID,
						})
					}
				case depStateFailed, depStateExpired:
					at := entry.changedAt(batch)
					if now.Sub(at) <= recentFailureWindow {
						failures = append(failures, dto.DependencyRequestIssueResult{
							UniqueID: entry.UniqueID, BatchID: batch.BatchID, State: entry.State, Detail: entry.Detail, UpdatedAt: at.UTC(),
						})
					}
				}
			}
		}
		return changed
	})
	if err != nil {
		slog.Warn("reading dependency requests failed", "game", gameID, "err", err)
		return nil, nil
	}
	kept := pending[:0]
	for _, entry := range pending {
		if modFolderPresent(gameID, entry.ModName) {
			kept = append(kept, entry)
		}
	}
	sort.SliceStable(failures, func(i, j int) bool { return failures[i].UpdatedAt.After(failures[j].UpdatedAt) })
	if len(failures) > recentFailureLimit {
		failures = failures[:recentFailureLimit]
	}
	return kept, failures
}

// failEnablesOutsideModlist fails profileName's pending enables whose mod is absent from the profile's modlist, read under the caller's request lock so an install registered before its entry update is always seen, and reports whether any changed.
func (md *ModDependencyService) failEnablesOutsideModlist(doc *depRequestsDoc, gameID, profileName string, now time.Time) bool {
	waiting := false
	doc.each(func(batch *depBatch, entry *depEntry) {
		if batch.Profile == profileName && entry.State == depStateEnablePending && entry.ModName != "" {
			waiting = true
		}
	})
	if !waiting {
		return false
	}
	inModlist, ok := md.profileModNames(gameID, profileName)
	if !ok {
		return false
	}
	changed := false
	doc.each(func(batch *depBatch, entry *depEntry) {
		if batch.Profile == profileName && entry.State == depStateEnablePending && entry.ModName != "" && !inModlist[entry.ModName] {
			entry.set(depStateFailed, detailNotInModlist+": "+entry.ModName+" is no longer in the profile's mod list", now)
			changed = true
		}
	})
	return changed
}

// profileModNames returns the mod folder names in profileName's modlist, and false when the modlist cannot be read.
func (md *ModDependencyService) profileModNames(gameID, profileName string) (map[string]bool, bool) {
	_, entries, err := md.s.profileMgr.Load(gameID, profileName)
	if err != nil {
		slog.Warn("reading the modlist for pending dependency enables failed; keeping them", "game", gameID, "profile", profileName, "err", err)
		return nil, false
	}
	names := make(map[string]bool, len(entries))
	for _, entry := range entries {
		names[entry.Name] = true
	}
	return names, true
}

// dropCheck removes a content-check extraction once its landing no longer uses it.
func (md *ModDependencyService) dropCheck(root string) {
	if err := os.RemoveAll(root); err != nil {
		slog.Warn("removing a dependency content-check extraction failed", "path", root, "err", err)
	}
}

// modFolderPresent reports whether modName is a valid mod folder name that exists as a directory in gameID's store.
func modFolderPresent(gameID, modName string) bool {
	if download.ValidateTargetModName(modName) != nil {
		return false
	}
	info, err := os.Stat(filepath.Join(config.ModsDir(gameID), modName))
	return err == nil && info.IsDir()
}
