package vfs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/parka/gorganizer/internal/atomicfile"
)

const deactivationMagic = "gorganizer-deactivating"

var deactivationStep = func(int) error { return nil }
var removeRetiredFarm = os.RemoveAll

type directoryIdentity struct {
	Dev uint64 `json:"dev"`
	Ino uint64 `json:"ino"`
}

type deactivationJournal struct {
	SchemaVersion int               `json:"schema_version"`
	Magic         string            `json:"magic"`
	GameID        string            `json:"game_id"`
	FarmID        string            `json:"farm_id"`
	Farm          directoryIdentity `json:"farm"`
	Backup        directoryIdentity `json:"backup"`
	CreatedAt     time.Time         `json:"created_at"`
}

func deactivationJournalPath(dataPath string) string { return dataPath + deactivatingSuffix }
func retiredFarmPath(dataPath string) string         { return dataPath + retiredSuffix }

// directoryAt reports the identity of a real directory without following a symlink.
func directoryAt(path string) (directoryIdentity, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return directoryIdentity{}, false, nil
	}
	if err != nil {
		return directoryIdentity{}, false, err
	}
	if !info.IsDir() {
		return directoryIdentity{}, true, nil
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return directoryIdentity{}, true, fmt.Errorf("unsupported directory identity for %s", path)
	}
	return directoryIdentity{Dev: uint64(stat.Dev), Ino: stat.Ino}, true, nil
}

// readDeactivationJournal loads and validates a journal without following a symlink.
func readDeactivationJournal(path string) (*deactivationJournal, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("checking deactivation journal: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("deactivation journal is not a regular file")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading deactivation journal: %w", err)
	}
	var j deactivationJournal
	if err := json.Unmarshal(body, &j); err != nil {
		return nil, fmt.Errorf("parsing deactivation journal: %w", err)
	}
	if j.SchemaVersion != 1 || j.Magic != deactivationMagic || j.Farm.Dev == 0 || j.Farm.Ino == 0 ||
		j.Backup.Dev == 0 || j.Backup.Ino == 0 || j.Farm == j.Backup || j.CreatedAt.IsZero() {
		return nil, fmt.Errorf("invalid or unsupported deactivation journal")
	}
	return &j, nil
}

// retireFarm records the directory identities before replacing the farm with the original Data directory.
func retireFarm(dataPath, backupPath string, s *Sentinel) error {
	if s == nil {
		return fmt.Errorf("retiring farm: missing sentinel")
	}
	for _, path := range []string{deactivationJournalPath(dataPath), retiredFarmPath(dataPath)} {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("retiring farm: %s already exists", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("checking %s: %w", path, err)
		}
	}
	farm, farmExists, err := directoryAt(dataPath)
	if err != nil {
		return fmt.Errorf("checking Data: %w", err)
	}
	backup, backupExists, err := directoryAt(backupPath)
	if err != nil {
		return fmt.Errorf("checking Data.orig: %w", err)
	}
	sentinelBackup, err := filepath.Abs(s.BackupPath)
	if err != nil {
		return fmt.Errorf("resolving sentinel backup: %w", err)
	}
	resolvedBackup, err := filepath.Abs(backupPath)
	if err != nil {
		return fmt.Errorf("resolving Data.orig: %w", err)
	}
	if !farmExists || !backupExists || farm.Dev == 0 || backup.Dev == 0 || farm == backup || sentinelBackup != resolvedBackup {
		return fmt.Errorf("retiring farm: Data and Data.orig do not match the mounted farm")
	}
	j := &deactivationJournal{
		SchemaVersion: 1,
		Magic:         deactivationMagic,
		GameID:        s.GameID,
		FarmID:        s.FarmID,
		Farm:          farm,
		Backup:        backup,
		CreatedAt:     time.Now().UTC(),
	}
	body, err := json.Marshal(j)
	if err != nil {
		return fmt.Errorf("marshalling deactivation journal: %w", err)
	}
	if _, err := atomicfile.WriteFileDurable(deactivationJournalPath(dataPath), body, 0644); err != nil {
		return fmt.Errorf("writing deactivation journal: %w", err)
	}
	if err := deactivationStep(1); err != nil {
		return err
	}
	return resumeFarmRetirement(dataPath, backupPath, j)
}

// resumeFarmRetirement completes a recorded teardown after checking every sibling's directory identity.
func resumeFarmRetirement(dataPath, backupPath string, j *deactivationJournal) error {
	retired := retiredFarmPath(dataPath)
	parent := filepath.Dir(dataPath)
	for {
		dataID, dataExists, dataErr := directoryAt(dataPath)
		backupID, backupExists, backupErr := directoryAt(backupPath)
		retiredID, retiredExists, retiredErr := directoryAt(retired)
		if err := errors.Join(dataErr, backupErr, retiredErr); err != nil {
			return fmt.Errorf("%w: checking Data, Data.orig and Data.gorganizer-retired: %v", errDeactivationMismatch, err)
		}
		switch {
		case dataExists && dataID == j.Farm && backupExists && backupID == j.Backup && !retiredExists:
			if err := os.Rename(dataPath, retired); err != nil {
				return fmt.Errorf("retiring Data: %w", err)
			}
			if err := atomicfile.SyncDir(parent); err != nil {
				return fmt.Errorf("syncing retired Data: %w", err)
			}
			if err := deactivationStep(2); err != nil {
				return err
			}
		case !dataExists && backupExists && backupID == j.Backup && retiredExists && retiredID == j.Farm:
			if err := os.Rename(backupPath, dataPath); err != nil {
				return fmt.Errorf("restoring Data.orig: %w", err)
			}
			if err := atomicfile.SyncDir(parent); err != nil {
				return fmt.Errorf("syncing restored Data: %w", err)
			}
			if err := deactivationStep(3); err != nil {
				return err
			}
		case dataExists && dataID == j.Backup && !backupExists && retiredExists && retiredID == j.Farm:
			if err := removeRetiredFarm(retired); err != nil {
				return fmt.Errorf("removing retired farm: %w", err)
			}
			if err := atomicfile.SyncDir(parent); err != nil {
				return fmt.Errorf("syncing removed farm: %w", err)
			}
			if err := deactivationStep(4); err != nil {
				return err
			}
		case dataExists && dataID == j.Backup && !backupExists && !retiredExists:
			if err := atomicfile.RemoveDurable(deactivationJournalPath(dataPath)); err != nil {
				return fmt.Errorf("removing deactivation journal: %w", err)
			}
			if err := deactivationStep(5); err != nil {
				return err
			}
			return nil
		default:
			return errDeactivationMismatch
		}
	}
}
