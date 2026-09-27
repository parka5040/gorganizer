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

// CleanupStale heals dataPath after a prior daemon crash; returns Pending for ambiguous states.
func CleanupStale(dataPath string) (RecoveryOutcome, error) {
	var outcome RecoveryOutcome

	resolved, err := filepath.Abs(dataPath)
	if err != nil {
		return outcome, fmt.Errorf("resolving %q: %w", dataPath, err)
	}
	backupPath := resolved + farmBackupSuffix
	journalPath := deactivationJournalPath(resolved)
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
		if err := resumeFarmRetirement(resolved, backupPath, j); err != nil {
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
	if _, statErr := os.Lstat(resolved); errors.Is(statErr, os.ErrNotExist) {
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
	} else if statErr != nil {
		return outcome, fmt.Errorf("checking %s: %w", resolved, statErr)
	}

	s, sErr := ReadSentinel(resolved)
	var vErr error
	if sErr == nil {
		vErr = ValidateSentinel(s)
	}
	validFarm := sErr == nil && vErr == nil
	activatingPath := activatingIntentPath(resolved)
	if _, iErr := ReadIntent(activatingPath); iErr == nil {
		if validFarm {
			if err := RemoveIntent(activatingPath); err != nil {
				return outcome, fmt.Errorf("removing committed activation intent: %w", err)
			}
		} else {
			if _, err := os.Stat(backupPath); err != nil {
				slog.Info("interrupted activation with no backup — leaving Data as-is", "path", resolved)
				if err := RemoveIntent(activatingPath); err != nil {
					return outcome, fmt.Errorf("removing activation intent: %w", err)
				}
				return outcome, nil
			}
			if err := os.RemoveAll(resolved); err != nil {
				return outcome, fmt.Errorf("removing interrupted activation at %s: %w", resolved, err)
			}
			if err := renameActivationBackup(backupPath, resolved); err != nil {
				return outcome, fmt.Errorf("rolling back interrupted activation of %s from %s: %w",
					resolved, backupPath, err)
			}
			if err := RemoveIntent(activatingPath); err != nil {
				return outcome, fmt.Errorf("removing rolled-back activation intent: %w", err)
			}
			slog.Info("rolled back interrupted activation", "path", resolved)
			outcome.Restored = true
			return outcome, nil
		}
	} else if !errors.Is(iErr, ErrIntentMissing) {
		slog.Warn("could not read activation intent during recovery", "path", resolved, "err", iErr)
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
	if validFarm || plainOriginal {
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
			if s.SchemaVersion >= 2 && s.OverwriteRoot != "" {
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
			if err := retireFarm(resolved, backupPath, s); err != nil {
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

// RestoreFromBackup performs the user-confirmed rm -rf Data, mv Data.orig → Data.
func RestoreFromBackup(dataPath string) error {
	resolved, err := filepath.Abs(dataPath)
	if err != nil {
		return fmt.Errorf("resolving %q: %w", dataPath, err)
	}
	backupPath := resolved + farmBackupSuffix
	if _, err := os.Stat(backupPath); err != nil {
		return fmt.Errorf("RestoreFromBackup: no backup at %s: %w", backupPath, err)
	}
	slog.Info("RestoreFromBackup: removing Data/", "path", resolved)
	if err := os.RemoveAll(resolved); err != nil {
		return fmt.Errorf("removing %s: %w", resolved, err)
	}
	slog.Info("RestoreFromBackup: renaming backup", "from", backupPath, "to", resolved)
	if err := os.Rename(backupPath, resolved); err != nil {
		return fmt.Errorf("renaming %s to %s: %w", backupPath, resolved, err)
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
