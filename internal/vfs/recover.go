package vfs

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/parka/gorganizer/internal/atomicfile"
)

type FuseMountInfo struct {
	Mountpoint string
	FSType     string
	Source     string
}

type RecoveryOutcome struct {
	FuseUnmounted bool
	Restored      bool
	Pending       *RecoveryPending
}

type RecoveryPending struct {
	DataPath   string
	BackupPath string
	Reason     string
}

// DetectFuseMount returns the live FUSE mount at dataPath, or (nil, nil) when none.
func DetectFuseMount(dataPath string) (*FuseMountInfo, error) {
	resolved, err := filepath.Abs(dataPath)
	if err != nil {
		return nil, fmt.Errorf("resolving %q: %w", dataPath, err)
	}

	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, fmt.Errorf("reading /proc/self/mountinfo: %w", err)
	}
	defer f.Close()

	return parseMountinfo(f, resolved), nil
}

// parseMountinfo extracts the first FUSE mount whose mountpoint matches target.
func parseMountinfo(r io.Reader, target string) *FuseMountInfo {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		dashIdx := -1
		for i, f := range fields {
			if f == "-" {
				dashIdx = i
				break
			}
		}
		if dashIdx < 0 || dashIdx+2 >= len(fields) {
			continue
		}
		mountpoint := unescapeMountinfoField(fields[4])
		fstype := fields[dashIdx+1]
		source := fields[dashIdx+2]

		if mountpoint != target {
			continue
		}
		if !strings.HasPrefix(fstype, "fuse") {
			continue
		}
		return &FuseMountInfo{
			Mountpoint: mountpoint,
			FSType:     fstype,
			Source:     source,
		}
	}
	return nil
}

// unescapeMountinfoField decodes kernel octal-escapes (\040 → space, etc).
func unescapeMountinfoField(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			n := int(s[i+1]-'0')*64 + int(s[i+2]-'0')*8 + int(s[i+3]-'0')
			if n >= 0 && n < 256 {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

var renameActivationBackup = os.Rename

const teardownCaptureFailureReason = "Gorganizer couldn't save files written during the last session, so it left the mod folder in place. Free some disk space, then restart Gorganizer."
const foreignActivationDataReason = "An interrupted mod activation left a Data folder Gorganizer did not create. Check Data and Data.orig before restoring."

// CleanupStale heals dataPath after a prior daemon crash; returns Pending for ambiguous states.
func CleanupStale(dataPath string, capture ...CaptureOptions) (RecoveryOutcome, error) {
	var outcome RecoveryOutcome
	var opts CaptureOptions
	if len(capture) > 0 {
		opts = capture[0]
	}

	resolved, err := filepath.Abs(dataPath)
	if err != nil {
		return outcome, fmt.Errorf("resolving %q: %w", dataPath, err)
	}
	backupPath := resolved + farmBackupSuffix
	journalPath := deactivationJournalPath(resolved)
	if record, recordErr := readRestoreRecord(resolved); recordErr != nil || record != nil {
		outcome.Pending = &RecoveryPending{
			DataPath: resolved, BackupPath: backupPath,
			Reason: "A confirmed restore was interrupted. Check Data and Data.orig before continuing.",
		}
		if recordErr != nil {
			return outcome, nil
		}
		_, backupExists, backupErr := directoryAt(backupPath)
		data, dataExists, dataErr := directoryAt(resolved)
		if err := errors.Join(backupErr, dataErr); err != nil {
			return outcome, fmt.Errorf("checking interrupted restore: %w", err)
		}
		if !backupExists && dataExists && data == record.Backup {
			if err := finishRestoreCleanup(resolved); err != nil {
				return outcome, fmt.Errorf("finishing interrupted restore: %w", err)
			}
			outcome.Pending = nil
			outcome.Restored = true
		}
		return outcome, nil
	}
	if _, statErr := os.Lstat(journalPath); !errors.Is(statErr, os.ErrNotExist) {
		outcome.Pending = &RecoveryPending{
			DataPath:   resolved,
			BackupPath: backupPath,
			Reason:     "An interrupted mod removal left folders that do not match its record. Check Data, Data.orig and Data.gorganizer-retired before restoring.",
		}
		if statErr != nil {
			return outcome, nil
		}
		j, readErr := readDeactivationJournal(journalPath)
		if readErr != nil {
			return outcome, nil
		}
		if opts.PreserveInto != "" && j.Capture.PreserveInto == "" {
			farm, exists, err := directoryAt(resolved)
			if err != nil {
				return outcome, fmt.Errorf("checking interrupted farm for preservation: %w", err)
			}
			if exists && farm == j.Farm {
				j.Capture = opts
				j.DataCaptured = false
				j.RetiredCaptured = false
				if err := writeDeactivationJournal(resolved, j); err != nil {
					return outcome, fmt.Errorf("updating preservation decision: %w", err)
				}
			} else {
				outcome.Pending.Reason = "Steam changed after mod removal began. Check the retired farm before completing recovery."
				return outcome, nil
			}
		}
		if err := resumeFarmRetirement(resolved, backupPath, j, false); err != nil {
			if errors.Is(err, ErrCaptureFailed) {
				outcome.Pending.Reason = teardownCaptureFailureReason
				return outcome, nil
			}
			if errors.Is(err, errDeactivationMismatch) {
				return outcome, nil
			}
			return RecoveryOutcome{}, fmt.Errorf("resuming interrupted mod removal: %w", err)
		}
		outcome.Pending = nil
		outcome.Restored = true
		return outcome, nil
	}
	if _, statErr := os.Lstat(retiredFarmPath(resolved)); !errors.Is(statErr, os.ErrNotExist) {
		outcome.Pending = &RecoveryPending{
			DataPath:   resolved,
			BackupPath: backupPath,
			Reason:     "A retired mod folder has no removal record. Check Data and Data.gorganizer-retired before restoring.",
		}
		return outcome, nil
	}

	mount, err := DetectFuseMount(resolved)
	if err != nil {
		slog.Warn("could not check for stale FUSE mount, continuing with backup restore",
			"path", resolved, "err", err)
	}

	if mount != nil {
		slog.Info("stale FUSE mount detected, attempting unmount",
			"path", mount.Mountpoint, "fstype", mount.FSType, "source", mount.Source)
		if err := unmountWithFallbacks(resolved); err != nil {
			return outcome, fmt.Errorf("unmounting stale FUSE at %s: %w", resolved, err)
		}
		outcome.FuseUnmounted = true
	}

	staging := stagingDirPath(resolved)
	oldFarm := oldFarmPath(resolved)
	applyPath := applyingIntentPath(resolved)
	activatingPath := activatingIntentPath(resolved)
	activation, activationErr := ReadIntent(activatingPath)
	if _, statErr := os.Lstat(resolved); errors.Is(statErr, os.ErrNotExist) &&
		!(activationErr == nil && activation.SchemaVersion == 2) {
		for _, candidate := range []string{oldFarm, staging} {
			s, readErr := ReadSentinel(candidate)
			if readErr != nil || ValidateSentinel(s) != nil {
				continue
			}
			if err := os.Rename(candidate, resolved); err != nil {
				return outcome, fmt.Errorf("restoring stranded farm from %s: %w", candidate, err)
			}
			slog.Info("restored stranded farm to Data", "from", candidate, "to", resolved)
			break
		}
		if _, statErr := os.Lstat(resolved); errors.Is(statErr, os.ErrNotExist) {
			for _, sibling := range []string{oldFarm, staging, applyPath} {
				if _, err := os.Lstat(sibling); err == nil {
					outcome.Pending = &RecoveryPending{
						DataPath:   resolved,
						BackupPath: backupPath,
						Reason:     "Data/ is missing while folders from an unfinished mod change remain. Check these folders before restoring the original files.",
					}
					return outcome, nil
				} else if !errors.Is(err, os.ErrNotExist) {
					return outcome, fmt.Errorf("checking transition sibling %s: %w", sibling, err)
				}
			}
		} else if statErr != nil {
			return outcome, fmt.Errorf("checking %s: %w", resolved, statErr)
		}
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return outcome, fmt.Errorf("checking %s: %w", resolved, statErr)
	}

	s, sErr := ReadSentinel(resolved)
	var vErr error
	if sErr == nil {
		vErr = ValidateSentinel(s)
	}
	validFarm := sErr == nil && vErr == nil
	if activationErr == nil {
		if validFarm {
			if err := RemoveIntent(activatingPath); err != nil {
				return outcome, fmt.Errorf("removing committed activation intent: %w", err)
			}
		} else if activation.SchemaVersion == 2 {
			if activation.Kind != IntentActivating || activation.DataPath != resolved || activation.BackupPath != backupPath {
				outcome.Pending = &RecoveryPending{
					DataPath: resolved, BackupPath: backupPath,
					Reason: "An interrupted mod activation left folders that do not match its record. Check Data and Data.orig before restoring.",
				}
				return outcome, nil
			}
			data, dataExists, dataErr := directoryAt(resolved)
			backup, backupExists, backupErr := directoryAt(backupPath)
			if err := errors.Join(dataErr, backupErr); err != nil {
				return outcome, fmt.Errorf("checking interrupted activation directories: %w", err)
			}
			switch {
			case dataExists && data == activation.Original && !backupExists:
				if err := syncFarmParent(filepath.Dir(resolved)); err != nil {
					return outcome, fmt.Errorf("syncing original Data: %w", err)
				}
				if err := RemoveIntent(activatingPath); err != nil {
					return outcome, fmt.Errorf("removing activation intent: %w", err)
				}
				return outcome, nil
			case backupExists && backup == activation.Original && dataExists &&
				(activation.Farm == nil || data != *activation.Farm):
				outcome.Pending = &RecoveryPending{
					DataPath: resolved, BackupPath: backupPath, Reason: foreignActivationDataReason,
				}
				return outcome, nil
			case backupExists && backup == activation.Original && (!dataExists || data != activation.Original):
				if err := rollbackActivation(resolved, backupPath, activatingPath, activation.Original, activation.Farm); err != nil {
					return outcome, fmt.Errorf("rolling back interrupted activation: %w", err)
				}
				outcome.Restored = true
				return outcome, nil
			default:
				outcome.Pending = &RecoveryPending{
					DataPath: resolved, BackupPath: backupPath,
					Reason: "An interrupted mod activation left folders that do not match its record. Check Data and Data.orig before restoring.",
				}
				return outcome, nil
			}
		} else {
			if _, err := os.Stat(backupPath); err != nil {
				if err := RemoveIntent(activatingPath); err != nil {
					return outcome, fmt.Errorf("removing activation intent: %w", err)
				}
				return outcome, nil
			}
			if err := os.RemoveAll(resolved); err != nil {
				return outcome, fmt.Errorf("removing interrupted activation at %s: %w", resolved, err)
			}
			if err := renameActivationBackup(backupPath, resolved); err != nil {
				return outcome, fmt.Errorf("rolling back interrupted activation of %s from %s: %w", resolved, backupPath, err)
			}
			if err := syncFarmParent(filepath.Dir(resolved)); err != nil {
				return outcome, fmt.Errorf("syncing restored Data: %w", err)
			}
			if err := RemoveIntent(activatingPath); err != nil {
				return outcome, fmt.Errorf("removing rolled-back activation intent: %w", err)
			}
			outcome.Restored = true
			return outcome, nil
		}
	} else if !errors.Is(activationErr, ErrIntentMissing) {
		outcome.Pending = &RecoveryPending{
			DataPath: resolved, BackupPath: backupPath,
			Reason: "An interrupted mod activation has a record Gorganizer cannot read. Check Data.gorganizer-activating before restoring.",
		}
		return outcome, nil
	}

	apply, applyErr := ReadIntent(applyPath)
	if applyErr == nil && apply.SchemaVersion == 2 {
		pending, err := reconcileApplyIntent(resolved, backupPath, staging, oldFarm, applyPath, s, validFarm, apply)
		if err != nil {
			return outcome, fmt.Errorf("reconciling interrupted apply: %w", err)
		}
		if pending != nil {
			outcome.Pending = pending
			return outcome, nil
		}
	} else if applyErr != nil && !errors.Is(applyErr, ErrIntentMissing) {
		outcome.Pending = &RecoveryPending{
			DataPath: resolved, BackupPath: backupPath,
			Reason: "An unfinished mod change has a record Gorganizer cannot read. Check Data.gorganizer-applying before continuing.",
		}
		return outcome, nil
	}

	plainOriginal := errors.Is(sErr, ErrSentinelMissing)
	if plainOriginal {
		entries, readErr := os.ReadDir(resolved)
		plainOriginal = readErr == nil
		if _, err := os.Stat(backupPath); err == nil {
			plainOriginal = plainOriginal && len(entries) == 0
		}
		for _, entry := range entries {
			if IsFarmMetadataFile(entry.Name()) {
				plainOriginal = false
			}
		}
	}
	if !validFarm {
		for _, candidate := range []string{oldFarm, staging} {
			candidateSentinel, readErr := ReadSentinel(candidate)
			if readErr == nil && ValidateSentinel(candidateSentinel) == nil {
				outcome.Pending = &RecoveryPending{
					DataPath:   resolved,
					BackupPath: backupPath,
					Reason:     "An unfinished mod change left a recoverable mod folder next to Data/. Check it before restoring the original files.",
				}
				return outcome, nil
			}
		}
	}
	if (validFarm || plainOriginal) && !(applyErr == nil && apply.SchemaVersion == 2) {
		for _, sibling := range []string{staging, oldFarm} {
			if err := os.RemoveAll(sibling); err != nil {
				return outcome, fmt.Errorf("removing transition sibling %s: %w", sibling, err)
			}
		}
		if err := RemoveIntent(applyPath); err != nil {
			return outcome, fmt.Errorf("removing apply intent: %w", err)
		}
	}

	if sErr == nil {
		if validFarm {
			slog.Info("found valid overlay sentinel from prior run — restoring",
				"path", resolved, "backup_path", s.BackupPath,
				"prior_pid", s.ActivationPID, "started_at", s.ActivationStartedAt)
			if opts.PreserveInto == "" && s.SchemaVersion >= 2 && s.OverwriteRoot != "" {
				if moved, capErr := CaptureNewFiles(resolved, s.OverwriteRoot); capErr != nil {
					slog.Warn("recovery capture failed — refusing to destroy Data",
						"path", resolved, "err", capErr)
					outcome.Pending = &RecoveryPending{
						DataPath:   resolved,
						BackupPath: backupPath,
						Reason: fmt.Sprintf("could not capture new writes into the overwrite mod during "+
							"recovery (%v). Confirm restore only if you accept discarding any un-captured "+
							"files written during the previous session.", capErr),
					}
					return outcome, nil
				} else if moved > 0 {
					slog.Info("recovery captured new writes into overwrite mod",
						"path", resolved, "count", moved, "overwrite_root", s.OverwriteRoot)
				}
			}
			if err := retireFarm(resolved, backupPath, s, false, opts); err != nil {
				if errors.Is(err, ErrCaptureFailed) {
					outcome.Pending = &RecoveryPending{DataPath: resolved, BackupPath: backupPath, Reason: teardownCaptureFailureReason}
					return outcome, nil
				}
				return outcome, fmt.Errorf("retiring crashed overlay at %s: %w", resolved, err)
			}
			slog.Info("overlay crash recovery complete", "path", resolved)
			outcome.Restored = true
			return outcome, nil
		} else if errors.Is(vErr, ErrSentinelInvalid) {
			slog.Warn("Data/ contains a sentinel that failed validation — surfacing as recovery-pending",
				"path", resolved, "err", vErr)
			outcome.Pending = &RecoveryPending{
				DataPath:   resolved,
				BackupPath: backupPath,
				Reason: fmt.Sprintf("Data/ holds a sentinel we don't recognize (%v). "+
					"Confirm restore to wipe Data/ and rename Data.orig/ back.", vErr),
			}
			return outcome, nil
		}
	} else if !errors.Is(sErr, ErrSentinelMissing) {
		slog.Warn("could not read sentinel during recovery", "path", resolved, "err", sErr)
	}

	backupExists := false
	if _, err := os.Stat(backupPath); err == nil {
		backupExists = true
	}

	if backupExists {
		if info, statErr := os.Stat(resolved); statErr == nil && info.IsDir() {
			entries, readErr := os.ReadDir(resolved)
			switch {
			case readErr != nil:
				slog.Warn("could not read mountpoint, leaving alone",
					"path", resolved, "err", readErr)
				return outcome, nil
			case len(entries) == 0:
				if rmErr := os.Remove(resolved); rmErr != nil {
					return outcome, fmt.Errorf("removing empty mountpoint %s: %w", resolved, rmErr)
				}
				slog.Info("removed empty mountpoint", "path", resolved)
			default:
				slog.Warn("Data/ is non-empty alongside Data.orig/ — surfacing as recovery-pending",
					"data", resolved, "backup", backupPath, "entries", len(entries))
				outcome.Pending = &RecoveryPending{
					DataPath:   resolved,
					BackupPath: backupPath,
					Reason: fmt.Sprintf("Data/ has %d entries but no gorganizer sentinel, "+
						"and Data.orig/ is present. Confirm restore to wipe Data/ "+
						"and rename Data.orig/ back, OR keep Data/ and discard the backup manually.",
						len(entries)),
				}
				return outcome, nil
			}
		}

		slog.Info("restoring backup", "from", backupPath, "to", resolved)
		if err := os.Rename(backupPath, resolved); err != nil {
			return outcome, fmt.Errorf("restoring %s from %s: %w", resolved, backupPath, err)
		}
		slog.Info("recovery complete", "path", resolved)
		outcome.Restored = true
	}

	return outcome, nil
}

// reconcileApplyIntent removes only the farm sibling identified by the recorded exchange.
func reconcileApplyIntent(dataPath, backupPath, staging, oldFarm, applyPath string, live *Sentinel, validFarm bool, in *ActivationIntent) (*RecoveryPending, error) {
	pending := &RecoveryPending{
		DataPath: dataPath, BackupPath: backupPath,
		Reason: "An unfinished mod change left a folder Gorganizer cannot identify. Check Data.gorganizer-staging before continuing.",
	}
	if !validFarm || in.Kind != IntentApplying || in.DataPath != dataPath || in.BackupPath != backupPath || in.StagingPath != staging {
		return pending, nil
	}
	dataID, dataExists, err := directoryAt(dataPath)
	if err != nil {
		return nil, fmt.Errorf("checking active farm: %w", err)
	}
	if !dataExists || dataID.Dev == 0 {
		return pending, nil
	}
	var siblingFarmID string
	switch live.FarmID {
	case in.StagingFarmID:
		siblingFarmID = in.LiveFarmID
	case in.LiveFarmID:
		siblingFarmID = in.StagingFarmID
	default:
		return pending, nil
	}
	if _, err := os.Lstat(oldFarm); err == nil {
		return pending, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("checking old farm sibling: %w", err)
	}
	stagingID, exists, err := directoryAt(staging)
	if err != nil {
		return nil, fmt.Errorf("checking staging sibling: %w", err)
	}
	if exists {
		if stagingID.Dev == 0 || stagingID == dataID {
			return pending, nil
		}
		candidate, err := ReadSentinel(staging)
		if err != nil || ValidateSentinel(candidate) != nil || candidate.FarmID != siblingFarmID {
			return pending, nil
		}
		if err := os.RemoveAll(staging); err != nil {
			return nil, fmt.Errorf("removing recorded staging sibling: %w", err)
		}
	}
	if err := RemoveIntent(applyPath); err != nil {
		return nil, fmt.Errorf("removing apply intent: %w", err)
	}
	return nil, nil
}

// RestoreFromBackup captures recorded farm writes and clears teardown markers during a confirmed restore.
func RestoreFromBackup(dataPath string, capture ...CaptureOptions) error {
	resolved, err := filepath.Abs(dataPath)
	if err != nil {
		return fmt.Errorf("resolving %q: %w", dataPath, err)
	}
	backupPath := resolved + farmBackupSuffix
	retired := retiredFarmPath(resolved)
	journal := deactivationJournalPath(resolved)
	var opts CaptureOptions
	var teardown *deactivationJournal
	if len(capture) > 0 {
		opts = capture[0]
	}
	record, err := readRestoreRecord(resolved)
	if err != nil {
		return err
	}
	if record != nil {
		backup, backupExists, backupErr := directoryAt(backupPath)
		data, dataExists, dataErr := directoryAt(resolved)
		if err := errors.Join(backupErr, dataErr); err != nil {
			return fmt.Errorf("checking interrupted restore: %w", err)
		}
		if !backupExists && dataExists && data == record.Backup {
			return finishRestoreCleanup(resolved)
		}
		if !backupExists || backup != record.Backup || dataExists && (record.Farm.Dev == 0 || data != record.Farm) {
			return fmt.Errorf("confirmed restore needs review: Data and Data.orig do not match the restore record")
		}
		if dataExists {
			if _, err := os.Lstat(journal); err == nil {
				teardown, err = readDeactivationJournal(journal)
				if err != nil {
					return err
				}
				if teardown.Capture.PreserveInto != "" {
					opts = teardown.Capture
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("checking deactivation journal: %w", err)
			}
			if teardown != nil && teardown.DataCaptured && data == teardown.Farm {
				err = captureRecordedFarm(resolved, resolved, opts, true)
			} else {
				err = captureRetiringFarm(resolved, resolved, opts)
			}
			if err != nil {
				return err
			}
		}
		return restoreRecordedBackup(resolved, backupPath, record)
	}
	if _, err := os.Lstat(journal); err == nil {
		j, err := readDeactivationJournal(journal)
		if err != nil {
			return err
		}
		teardown = j
		if j.Capture.PreserveInto != "" {
			opts = j.Capture
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("checking deactivation journal: %w", err)
	}
	_, backupErr := os.Lstat(backupPath)
	if backupErr != nil && !errors.Is(backupErr, os.ErrNotExist) {
		return fmt.Errorf("checking backup %s: %w", backupPath, backupErr)
	}
	if errors.Is(backupErr, os.ErrNotExist) {
		data, exists, err := directoryAt(resolved)
		if err != nil || !exists || data.Dev == 0 {
			return fmt.Errorf("RestoreFromBackup: no backup at %s: %w", backupPath, os.ErrNotExist)
		}
		if _, err := ReadSentinel(resolved); !errors.Is(err, ErrSentinelMissing) {
			return fmt.Errorf("RestoreFromBackup: no original backup at %s", backupPath)
		}
		_, journalErr := os.Lstat(journal)
		_, retiredErr := os.Lstat(retired)
		if errors.Is(journalErr, os.ErrNotExist) && errors.Is(retiredErr, os.ErrNotExist) {
			return fmt.Errorf("RestoreFromBackup: no backup at %s: %w", backupPath, os.ErrNotExist)
		}
		if journalErr != nil && !errors.Is(journalErr, os.ErrNotExist) || retiredErr != nil && !errors.Is(retiredErr, os.ErrNotExist) {
			return fmt.Errorf("checking teardown markers: %w", errors.Join(journalErr, retiredErr))
		}
	} else {
		dataID, exists, err := directoryAt(resolved)
		if err != nil {
			return fmt.Errorf("checking Data before capture: %w", err)
		}
		if teardown != nil && teardown.DataCaptured && exists && dataID == teardown.Farm {
			err = captureRecordedFarm(resolved, resolved, opts, true)
		} else {
			err = captureRetiringFarm(resolved, resolved, opts)
		}
		if err != nil {
			return err
		}
	}
	retiredID, retiredExists, err := directoryAt(retired)
	if err != nil {
		return fmt.Errorf("checking retired farm before capture: %w", err)
	}
	if teardown != nil && teardown.RetiredCaptured && retiredExists && retiredID == teardown.Farm {
		err = captureRecordedFarm(retired, resolved, opts, true)
	} else {
		err = captureRetiringFarm(retired, resolved, opts)
	}
	if err != nil {
		return err
	}
	for _, sibling := range []string{stagingDirPath(resolved), oldFarmPath(resolved)} {
		s, err := ReadSentinel(sibling)
		if err != nil && !errors.Is(err, ErrSentinelMissing) {
			return fmt.Errorf("reading transition farm %s: %w", sibling, err)
		}
		if err == nil && (s.OverwriteRoot != "" || opts.PreserveInto != "") {
			if err := captureRetiringFarm(sibling, resolved, opts); err != nil {
				return err
			}
		}
	}
	if err := restoreStep(0); err != nil {
		return err
	}
	if backupErr == nil {
		backup, exists, err := directoryAt(backupPath)
		if err != nil || !exists || backup.Dev == 0 {
			return fmt.Errorf("checking original Data backup: %v", err)
		}
		farm, _, err := directoryAt(resolved)
		if err != nil {
			return fmt.Errorf("checking farm before restore: %w", err)
		}
		record := &restoreRecord{SchemaVersion: 1, DataPath: resolved, Backup: backup, Farm: farm}
		if err := writeRestoreRecord(resolved, record); err != nil {
			return err
		}
		if err := restoreStep(1); err != nil {
			return err
		}
		return restoreRecordedBackup(resolved, backupPath, record)
	}
	return finishRestoreCleanup(resolved)
}

// restoreRecordedBackup replaces the captured farm only while the backup still matches its durable identity.
func restoreRecordedBackup(resolved, backupPath string, record *restoreRecord) error {
	backup, exists, err := directoryAt(backupPath)
	if err != nil || !exists || backup != record.Backup {
		return fmt.Errorf("confirmed restore needs review: backup changed: %v", err)
	}
	data, dataExists, err := directoryAt(resolved)
	if err != nil || dataExists && (record.Farm.Dev == 0 || data != record.Farm) {
		return fmt.Errorf("confirmed restore needs review: Data changed: %v", err)
	}
	if dataExists {
		if err := os.RemoveAll(resolved); err != nil {
			return fmt.Errorf("removing %s: %w", resolved, err)
		}
	}
	if err := restoreStep(2); err != nil {
		return err
	}
	if err := os.Rename(backupPath, resolved); err != nil {
		return fmt.Errorf("renaming %s to %s: %w", backupPath, resolved, err)
	}
	if err := atomicfile.SyncDir(filepath.Dir(resolved)); err != nil {
		return fmt.Errorf("syncing restored Data: %w", err)
	}
	if err := restoreStep(3); err != nil {
		return err
	}
	return finishRestoreCleanup(resolved)
}

// finishRestoreCleanup removes the remaining transition siblings and removes the restore record last.
func finishRestoreCleanup(resolved string) error {
	retired := retiredFarmPath(resolved)
	journal := deactivationJournalPath(resolved)
	if err := removeRetiredFarm(retired); err != nil {
		return fmt.Errorf("removing retired farm: %w", err)
	}
	if err := atomicfile.SyncDir(filepath.Dir(resolved)); err != nil {
		return fmt.Errorf("syncing removed retired farm: %w", err)
	}
	if err := restoreStep(4); err != nil {
		return err
	}
	if err := atomicfile.RemoveDurable(journal); err != nil {
		return fmt.Errorf("removing deactivation journal: %w", err)
	}
	if err := restoreStep(5); err != nil {
		return err
	}
	for _, sibling := range []string{stagingDirPath(resolved), oldFarmPath(resolved)} {
		if err := os.RemoveAll(sibling); err != nil {
			return fmt.Errorf("removing transition sibling %s: %w", sibling, err)
		}
	}
	if err := atomicfile.SyncDir(filepath.Dir(resolved)); err != nil {
		return fmt.Errorf("syncing removed transition farms: %w", err)
	}
	if err := restoreStep(6); err != nil {
		return err
	}
	for _, intent := range []string{activatingIntentPath(resolved), applyingIntentPath(resolved)} {
		if err := RemoveIntent(intent); err != nil {
			return fmt.Errorf("removing transition intent: %w", err)
		}
	}
	if err := restoreStep(7); err != nil {
		return err
	}
	if err := atomicfile.RemoveDurable(resolved + restoringSuffix); err != nil {
		return fmt.Errorf("removing restore record: %w", err)
	}
	slog.Info("RestoreFromBackup: complete", "path", resolved)
	return nil
}

// unmountWithFallbacks tries fusermount3, fusermount, then `umount -l`.
func unmountWithFallbacks(path string) error {
	candidates := []struct {
		bin  string
		args []string
	}{
		{"fusermount3", []string{"-u", path}},
		{"fusermount", []string{"-u", path}},
		{"umount", []string{"-l", path}},
	}

	var lastErr error
	for _, c := range candidates {
		bin, lookupErr := exec.LookPath(c.bin)
		if lookupErr != nil {
			lastErr = fmt.Errorf("%s not on PATH", c.bin)
			continue
		}
		cmd := exec.Command(bin, c.args...)
		out, err := cmd.CombinedOutput()
		if err == nil {
			slog.Info("unmount succeeded",
				"tool", c.bin, "path", path, "output", strings.TrimSpace(string(out)))
			return nil
		}
		lastErr = fmt.Errorf("%s %v: %w (%s)",
			c.bin, c.args, err, strings.TrimSpace(string(out)))
		slog.Warn("unmount attempt failed", "tool", c.bin, "err", lastErr)
	}

	if lastErr == nil {
		lastErr = errors.New("no unmount tool available")
	}
	return lastErr
}
