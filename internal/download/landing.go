package download

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/config"
)

const landingSchemaVersion = 1

type landingIndexEntry struct {
	Path   string `json:"path"`
	ModID  int    `json:"mod_id"`
	FileID int    `json:"file_id"`
}

type landingRecord struct {
	SchemaVersion int               `json:"schema_version"`
	ID            string            `json:"id"`
	GameID        string            `json:"game_id"`
	ArchiveRel    string            `json:"archive_rel"`
	Size          int64             `json:"size"`
	PartDev       uint64            `json:"part_dev"`
	PartIno       uint64            `json:"part_ino"`
	Sidecar       ArchiveSidecar    `json:"sidecar"`
	IndexEntry    landingIndexEntry `json:"index_entry"`
	CreatedAt     time.Time         `json:"created_at"`
}

type FinishedLanding struct {
	Snapshot    DownloadSnapshot
	ArchivePath string
	Sidecar     ArchiveSidecar
}

type landingActions struct {
	rename      func(string, string) error
	saveSidecar func(string, ArchiveSidecar, time.Time) error
	upsertEntry func(string, IndexEntry) error
}

var landingMu sync.Map

// landingLock returns the per-game lock for writing and finishing landing records.
func landingLock(gameID string) *sync.Mutex {
	lock, _ := landingMu.LoadOrStore(gameID, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

// landingPath validates a download ID before forming its landing-record path.
func landingPath(gameID, id string) (string, error) {
	if !strings.HasPrefix(id, "dl-") || uuid.Validate(strings.TrimPrefix(id, "dl-")) != nil ||
		id != "dl-"+strings.ToLower(strings.TrimPrefix(id, "dl-")) || len(id) != len("dl-")+36 {
		return "", fmt.Errorf("invalid download ID")
	}
	return filepath.Join(config.DownloadsDir(gameID), ".gorganizer-landing", id+".json"), nil
}

// HasLanding reports whether an ID has an unfinished landing record.
func HasLanding(gameID, id string) (bool, error) {
	path, err := landingPath(gameID, id)
	if err != nil {
		return false, nil
	}
	info, err := os.Lstat(filepath.Dir(path))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking landing directory: %w", err)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("invalid landing directory")
	}
	_, err = os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// writeLanding durably saves the part identity and metadata before publishing the archive.
func writeLanding(record landingRecord) error {
	path, err := landingPath(record.GameID, record.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating landing directory: %w", err)
	}
	info, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("checking landing directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("invalid landing directory")
	}
	if info.Mode().Perm() != 0700 {
		if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
			return fmt.Errorf("securing landing directory: %w", err)
		}
	}
	if err := atomicfile.SyncDir(config.DownloadsDir(record.GameID)); err != nil {
		return fmt.Errorf("syncing landing directory entry: %w", err)
	}
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encoding landing record: %w", err)
	}
	if _, err := atomicfile.WriteFileDurable(path, data, 0600); err != nil {
		return fmt.Errorf("saving landing record: %w", err)
	}
	return nil
}

// loadLanding validates a landing record without changing it.
func loadLanding(gameID, id string) (landingRecord, bool, error) {
	path, err := landingPath(gameID, id)
	if err != nil {
		return landingRecord{}, false, err
	}
	dirInfo, err := os.Lstat(filepath.Dir(path))
	if errors.Is(err, os.ErrNotExist) {
		return landingRecord{}, false, nil
	}
	if err != nil {
		return landingRecord{}, false, fmt.Errorf("checking landing directory: %w", err)
	}
	if !dirInfo.IsDir() {
		return landingRecord{}, false, fmt.Errorf("invalid landing directory")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return landingRecord{}, false, nil
	}
	if err != nil {
		return landingRecord{}, false, fmt.Errorf("checking landing record: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 1024*1024 {
		return landingRecord{}, true, fmt.Errorf("invalid landing record")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return landingRecord{}, true, fmt.Errorf("reading landing record: %w", err)
	}
	var record landingRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return landingRecord{}, true, fmt.Errorf("decoding landing record: %w", err)
	}
	if record.SchemaVersion != landingSchemaVersion || record.ID != id || record.GameID != gameID ||
		record.Size < 0 || record.PartDev == 0 || record.PartIno == 0 || record.CreatedAt.IsZero() ||
		record.IndexEntry.Path != record.ArchiveRel || record.Sidecar.SizeBytes != record.Size ||
		record.IndexEntry.ModID != record.Sidecar.ModID || record.IndexEntry.FileID != record.Sidecar.FileID {
		return landingRecord{}, true, fmt.Errorf("invalid landing record")
	}
	if _, err := resolveArchiveDestination(gameID, record.ArchiveRel); err != nil {
		return landingRecord{}, true, fmt.Errorf("invalid landing archive path: %w", err)
	}
	return record, true, nil
}

// fileMatchesLanding checks an archive or part against the identity recorded before renaming.
func fileMatchesLanding(path string, record landingRecord) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking landing file: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().IsRegular() && info.Size() == record.Size && uint64(stat.Dev) == record.PartDev && stat.Ino == record.PartIno, nil
}

// finishLandingLocked verifies and commits a record while its game lock and any manager destination claim are held.
func finishLandingLocked(record landingRecord, actions landingActions) (FinishedLanding, error) {
	archivePath, err := resolveArchiveDestination(record.GameID, record.ArchiveRel)
	if err != nil {
		return FinishedLanding{}, err
	}
	if err := checkArchiveFolder(archivePath, record.ArchiveRel); err != nil {
		return FinishedLanding{}, err
	}
	matched, err := fileMatchesLanding(archivePath, record)
	if err != nil {
		return FinishedLanding{}, err
	}
	if !matched {
		partPath := PartPath(archivePath)
		matched, err = fileMatchesLanding(partPath, record)
		if err != nil {
			return FinishedLanding{}, err
		}
		if !matched {
			return FinishedLanding{}, fmt.Errorf("archive missing or different from the saved download")
		}
		rename := actions.rename
		if rename == nil {
			rename = os.Rename
		}
		if err := rename(partPath, archivePath); err != nil {
			return FinishedLanding{}, fmt.Errorf("renaming archive part: %w", err)
		}
		if err := atomicfile.SyncDir(filepath.Dir(archivePath)); err != nil {
			return FinishedLanding{}, fmt.Errorf("syncing archive directory: %w", err)
		}
	}
	saveSidecar := actions.saveSidecar
	if saveSidecar == nil {
		saveSidecar = SaveSidecar
	}
	if err := saveSidecar(archivePath, record.Sidecar, record.CreatedAt); err != nil {
		return FinishedLanding{}, &ArchiveInformationSaveError{Err: fmt.Errorf("saving archive sidecar: %w", err)}
	}
	upsertEntry := actions.upsertEntry
	if upsertEntry == nil {
		upsertEntry = UpsertEntry
	}
	if err := upsertEntry(record.GameID, IndexEntry{Path: record.IndexEntry.Path, ModID: record.IndexEntry.ModID, FileID: record.IndexEntry.FileID}); err != nil {
		return FinishedLanding{}, &ArchiveInformationSaveError{Err: fmt.Errorf("saving downloads index: %w", err)}
	}
	if err := RemoveLedgerEntry(record.GameID, record.ID); err != nil {
		return FinishedLanding{}, &ArchiveInformationSaveError{Err: fmt.Errorf("removing download ledger entry: %w", err)}
	}
	path, _ := landingPath(record.GameID, record.ID)
	if err := atomicfile.RemoveDurable(path); err != nil {
		return FinishedLanding{}, fmt.Errorf("removing landing record: %w", err)
	}
	return FinishedLanding{Snapshot: DownloadSnapshot{
		ID: record.ID, GameID: record.GameID, ModName: record.Sidecar.ModName,
		BytesDownloaded: record.Size, BytesTotal: record.Size, Status: StatusDownloaded,
	}, ArchivePath: archivePath, Sidecar: record.Sidecar}, nil
}

// FinishLanding finishes a recorded download without a manager, API key or network access.
func FinishLanding(gameID, id string) (FinishedLanding, bool, error) {
	return finishLanding(gameID, id, nil)
}

// FinishLandingWithManager finishes a record while claiming its manager destination.
func FinishLandingWithManager(gameID, id string, manager *Manager) (FinishedLanding, bool, error) {
	return finishLanding(gameID, id, manager)
}

// finishLanding serializes finalization with other landings for the same game.
func finishLanding(gameID, id string, manager *Manager) (FinishedLanding, bool, error) {
	lock := landingLock(gameID)
	lock.Lock()
	defer lock.Unlock()
	record, present, err := loadLanding(gameID, id)
	if err != nil || !present {
		return FinishedLanding{}, present, err
	}
	if manager != nil {
		if !manager.claimDestination(gameID, record.ArchiveRel, id) {
			return FinishedLanding{}, true, ErrArchiveDownloadBusy
		}
		defer manager.releaseDestination(gameID, record.ArchiveRel, id)
	}
	finished, err := finishLandingLocked(record, landingActions{})
	return finished, true, err
}

// DiscardLanding removes a landing record when its download is explicitly removed.
func DiscardLanding(gameID, id string) error {
	path, err := landingPath(gameID, id)
	if err != nil {
		return err
	}
	lock := landingLock(gameID)
	lock.Lock()
	defer lock.Unlock()
	info, err := os.Lstat(filepath.Dir(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking landing directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("invalid landing directory")
	}
	return atomicfile.RemoveDurable(path)
}

// RecoverLandings finishes every recorded archive for configured games and logs any records that need attention.
func RecoverLandings(gameIDs []string) []FinishedLanding {
	var finished []FinishedLanding
	for _, gameID := range gameIDs {
		dir := filepath.Join(config.DownloadsDir(gameID), ".gorganizer-landing")
		info, err := os.Lstat(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			slog.Warn("checking archive landing directory failed", "game", gameID, "err", err)
			continue
		}
		if !info.IsDir() {
			slog.Warn("archive landing directory is not a directory", "game", gameID)
			continue
		}
		files, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			slog.Warn("reading archive landing directory failed", "game", gameID, "err", err)
			continue
		}
		for _, file := range files {
			if filepath.Ext(file.Name()) != ".json" {
				continue
			}
			id := strings.TrimSuffix(file.Name(), ".json")
			result, _, err := FinishLanding(gameID, id)
			if err != nil {
				slog.Warn("finishing an archive landing failed", "game", gameID, "id", id, "err", err)
				entries, ledgerErr := LoadLedger(gameID)
				if ledgerErr != nil {
					slog.Warn("reading unfinished landing ledger failed", "game", gameID, "err", ledgerErr)
					continue
				}
				for _, entry := range entries {
					if entry.ID == id && !entry.Terminal() {
						entry.Status = LedgerFailed
						entry.Error = (&LandingRecoveryError{Err: err}).Error()
						if saveErr := UpsertLedgerEntry(entry); saveErr != nil {
							slog.Warn("marking unfinished landing failed", "game", gameID, "id", id, "err", saveErr)
						}
						break
					}
				}
				continue
			}
			finished = append(finished, result)
		}
	}
	return finished
}
