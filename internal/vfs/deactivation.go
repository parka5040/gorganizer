package vfs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/parka/gorganizer/internal/atomicfile"
)

const deactivationMagic = "gorganizer-deactivating"
const currentDeactivationSchema = 2

var deactivationStep = func(int) error { return nil }
var removeRetiredFarm = os.RemoveAll

type directoryIdentity struct {
	Dev uint64 `json:"dev"`
	Ino uint64 `json:"ino"`
}

type deactivationJournal struct {
	SchemaVersion   int               `json:"schema_version"`
	Magic           string            `json:"magic"`
	GameID          string            `json:"game_id"`
	FarmID          string            `json:"farm_id"`
	Farm            directoryIdentity `json:"farm"`
	Backup          directoryIdentity `json:"backup"`
	CreatedAt       time.Time         `json:"created_at"`
	Capture         CaptureOptions    `json:"capture,omitempty"`
	DataCaptured    bool              `json:"data_captured,omitempty"`
	RetiredCaptured bool              `json:"retired_captured,omitempty"`
}

func deactivationJournalPath(dataPath string) string { return dataPath + deactivatingSuffix }
func retiredFarmPath(dataPath string) string         { return dataPath + retiredSuffix }

// RecoveryFarmCandidates returns the farm locations crash recovery can finish removing or restore to Data.
func RecoveryFarmCandidates(dataPath string) []string {
	return []string{retiredFarmPath(dataPath), oldFarmPath(dataPath), stagingDirPath(dataPath)}
}

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
	if j.SchemaVersion < 1 || j.SchemaVersion > currentDeactivationSchema || j.Magic != deactivationMagic ||
		j.Farm.Dev == 0 || j.Farm.Ino == 0 || j.Backup.Dev == 0 || j.Backup.Ino == 0 ||
		j.Farm == j.Backup || j.CreatedAt.IsZero() ||
		(j.SchemaVersion == 1 && (j.DataCaptured || j.RetiredCaptured)) {
		return nil, fmt.Errorf("invalid or unsupported deactivation journal")
	}
	if j.Capture.PreserveInto != "" &&
		(!strings.HasSuffix(path, deactivatingSuffix) ||
			j.Capture.PreserveInto != PreservedDir(strings.TrimSuffix(path, deactivatingSuffix)) ||
			uuid.Validate(j.Capture.BatchID) != nil || j.Capture.Baseline == nil || j.Capture.Current == nil) {
		return nil, fmt.Errorf("invalid preservation decision in deactivation journal")
	}
	return &j, nil
}

// writeDeactivationJournal durably records the state of a farm retirement.
func writeDeactivationJournal(dataPath string, j *deactivationJournal) error {
	j.SchemaVersion = currentDeactivationSchema
	body, err := json.Marshal(j)
	if err != nil {
		return fmt.Errorf("marshalling deactivation journal: %w", err)
	}
	if _, err := atomicfile.WriteFileDurable(deactivationJournalPath(dataPath), body, 0644); err != nil {
		return fmt.Errorf("writing deactivation journal: %w", err)
	}
	return nil
}

// captureRecordedFarm saves additional writes when its metadata remains available after an earlier capture.
func captureRecordedFarm(farmDir, dataPath string, opts CaptureOptions, captured bool) error {
	s, err := ReadSentinel(farmDir)
	if errors.Is(err, ErrSentinelMissing) {
		if captured {
			return nil
		}
		return fmt.Errorf("%w: farm metadata is missing before capture", ErrCaptureFailed)
	}
	if captured && err == nil && s.SchemaVersion == 3 {
		if _, err := os.Lstat(filepath.Join(farmDir, s.Manifest)); errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	return captureRetiringFarm(farmDir, dataPath, opts)
}

// captureRetiringFarm saves new writes from a recorded farm before it is removed.
func captureRetiringFarm(farmDir, dataPath string, opts CaptureOptions) error {
	s, err := ReadSentinel(farmDir)
	if err != nil {
		if opts.PreserveInto != "" {
			if _, statErr := os.Lstat(farmDir); errors.Is(statErr, os.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("%w: reading farm before preservation: %w", ErrCaptureFailed, err)
		}
		return nil
	}
	if opts.PreserveInto != "" {
		if err := preserveFarmOutput(farmDir, dataPath, s, opts); err != nil {
			return err
		}
		if err := writeMaintenance(dataPath, s, opts); err != nil {
			return fmt.Errorf("%w: saving Steam maintenance state: %w", ErrCaptureFailed, err)
		}
		return nil
	}
	if s.OverwriteRoot != "" {
		if _, err := CaptureNewFilesInto(farmDir, s.OverwriteRoot, false, false); err != nil {
			return fmt.Errorf("%w: saving writes from %s: %w", ErrCaptureFailed, farmDir, err)
		}
	}
	return nil
}

// retireFarm records the directory identities before replacing the farm with the original Data directory.
func retireFarm(dataPath, backupPath string, s *Sentinel, force bool, capture ...CaptureOptions) error {
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
		SchemaVersion: currentDeactivationSchema,
		Magic:         deactivationMagic,
		GameID:        s.GameID,
		FarmID:        s.FarmID,
		Farm:          farm,
		Backup:        backup,
		CreatedAt:     time.Now().UTC(),
	}
	if len(capture) > 0 {
		j.Capture = capture[0]
	}
	if err := writeDeactivationJournal(dataPath, j); err != nil {
		return err
	}
	if err := deactivationStep(1); err != nil {
		return err
	}
	return resumeFarmRetirement(dataPath, backupPath, j, force)
}

// resumeFarmRetirement completes a recorded teardown after checking every sibling's directory identity.
func resumeFarmRetirement(dataPath, backupPath string, j *deactivationJournal, force bool) error {
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
			if err := captureRecordedFarm(dataPath, dataPath, j.Capture, j.DataCaptured); err != nil {
				if !force {
					return err
				}
			} else if !j.DataCaptured {
				j.DataCaptured = true
				if err := writeDeactivationJournal(dataPath, j); err != nil {
					return err
				}
			}
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
			if err := captureRecordedFarm(retired, dataPath, j.Capture, j.RetiredCaptured); err != nil {
				if !force {
					return err
				}
			} else if !j.RetiredCaptured {
				j.RetiredCaptured = true
				if err := writeDeactivationJournal(dataPath, j); err != nil {
					return err
				}
			}
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
