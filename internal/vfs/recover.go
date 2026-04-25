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

// FuseMountInfo describes a live FUSE mount discovered at a given mountpoint.
// Returned by DetectFuseMount; consumed by CleanupStale.
type FuseMountInfo struct {
	Mountpoint string
	FSType     string // e.g. "fuse.gorganizer", "fuse"
	Source     string // mount source field — useful in logs to identify which daemon owned it
}

// RecoveryOutcome reports what CleanupStale did. The pending case is the
// only one that requires user action — every other state is either "no
// action needed" or "I already handled it". Consumers (the daemon, the
// CLI) decide whether to surface a prompt based on Pending.
type RecoveryOutcome struct {
	// FuseUnmounted is true when CleanupStale unmounted a stale FUSE
	// mount during the pass. Mostly logging context.
	FuseUnmounted bool

	// Restored is true when Data.orig was renamed back over Data without
	// asking. Happens for unambiguous states (no Data + Data.orig, or
	// valid sentinel + Data.orig).
	Restored bool

	// Pending is non-nil when the on-disk state is ambiguous and the
	// daemon must NOT auto-restore. The struct carries enough context
	// for the GUI to present a confirmation prompt.
	Pending *RecoveryPending
}

// RecoveryPending describes an on-disk state CleanupStale refused to act
// on because confirming would require user consent (an unrecognized Data
// alongside an intact Data.orig). The frontend reads Reason, DataPath,
// and BackupPath to render an actionable dialog.
type RecoveryPending struct {
	DataPath   string
	BackupPath string
	Reason     string // human-readable, e.g. "Data/ is non-empty alongside Data.orig/"
}

// DetectFuseMount returns a FuseMountInfo describing any live FUSE mount whose
// mountpoint exactly matches dataPath. Returns (nil, nil) when no FUSE mount
// is found at that path. Reads /proc/self/mountinfo so it works without root
// or any external tools.
//
// We only care about mountpoint string equality + a fstype starting with
// "fuse" — the typical stale-gorganizer case is "fuse.gorganizer", but a
// careful operator may have renamed the FsName. Anything fuse-prefixed at
// our mountpoint is, by construction, ours to clean up: nothing else has
// any business mounting a FUSE fs onto a Bethesda Data/ directory.
func DetectFuseMount(dataPath string) (*FuseMountInfo, error) {
	resolved, err := filepath.Abs(dataPath)
	if err != nil {
		return nil, fmt.Errorf("resolving %q: %w", dataPath, err)
	}

	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		// Non-Linux or unusual sandbox — caller should treat absence as
		// "no FUSE mount detected" and continue (most callers fall back
		// to checking Data.orig sibling directly).
		return nil, fmt.Errorf("reading /proc/self/mountinfo: %w", err)
	}
	defer f.Close()

	return parseMountinfo(f, resolved), nil
}

// parseMountinfo extracts the first FUSE mount whose mountpoint matches
// target. Split out for unit testing against canned fixtures.
//
// /proc/self/mountinfo line format (per kernel docs Documentation/filesystems/proc.rst):
//
//	36 35 98:0 /mnt1 /mnt2 rw,noatime master:1 - ext3 /dev/root rw,errors=continue
//	(1)(2)(3)   (4)   (5)      (6)      (7)   (8) (9)   (10)         (11)
//
// We need fields:
//
//	(5)  mount point
//	(9)  filesystem type (after the " - " separator, position is variable)
//	(10) mount source
//
// Optional fields between (7) and (8) make the " - " separator the only
// reliable boundary. Walk fields, find the dash, then the type is dash+1
// and the source is dash+2.
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

// unescapeMountinfoField decodes the kernel's octal-escape form for fields
// containing whitespace or special chars. Spaces become \040, tabs \011, etc.
// Bethesda install paths frequently have spaces ("Fallout New Vegas"), so this
// is load-bearing for matching.
func unescapeMountinfoField(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			// \NNN octal triple
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

// CleanupStale heals dataPath after an abnormal shutdown of a previous
// daemon. Handles three cases:
//
//  1. Legacy stale FUSE mount left over from the in-process FUSE backend
//     (kernel mount entry exists, userspace serving process is gone).
//     Resolved by fusermount, then directory cleanup.
//  2. New materialized overlay where the daemon died mid-session (sentinel
//     present, Data is a hardlink farm, Data.orig holds the original).
//     Resolved by rm -rf Data and rename Data.orig back. Write capture
//     is skipped at this layer — recovery doesn't know which mod is the
//     configured Overwrite target; the daemon surfaces the crash via the
//     usual logs. For a clean Deactivate flow with capture, see
//     MountManager.Deactivate.
//  3. Plain crash before activate could materialize (Data.orig exists,
//     Data does not). Resolved by renaming Data.orig back.
//
// For ambiguous states — an unrecognized non-empty Data/ alongside a
// non-missing Data.orig/ — CleanupStale returns a RecoveryOutcome with a
// non-nil Pending field and does NOT touch anything. The caller decides
// whether to prompt the user for consent before running
// RestoreFromBackup.
//
// Legacy wrapper: callers that don't care about the outcome detail can
// ignore the returned struct; a nil error still means "no critical
// failure", even when Pending is non-nil (ambiguity is not an error in
// itself).
func CleanupStale(dataPath string) (RecoveryOutcome, error) {
	var outcome RecoveryOutcome

	resolved, err := filepath.Abs(dataPath)
	if err != nil {
		return outcome, fmt.Errorf("resolving %q: %w", dataPath, err)
	}
	backupPath := resolved + ".orig"

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

	// Sentinel-based crash recovery: a Data dir with our sentinel means a
	// previous daemon activated this overlay and crashed before Deactivate
	// ran. The hardlink farm is harmless to read but we want to restore
	// the original Data so the game launches cleanly next time. Validate
	// before destroying — a sentinel-shaped JSON file from somewhere else
	// shouldn't fool us.
	if s, sErr := ReadSentinel(resolved); sErr == nil {
		if vErr := ValidateSentinel(s); vErr == nil {
			slog.Info("found valid overlay sentinel from prior run — restoring",
				"path", resolved, "backup_path", s.BackupPath,
				"prior_pid", s.ActivationPID, "started_at", s.ActivationStartedAt)
			if err := os.RemoveAll(resolved); err != nil {
				return outcome, fmt.Errorf("removing crashed overlay at %s: %w", resolved, err)
			}
			if _, statErr := os.Stat(s.BackupPath); statErr == nil {
				if err := os.Rename(s.BackupPath, resolved); err != nil {
					return outcome, fmt.Errorf("restoring %s from %s after crash: %w",
						resolved, s.BackupPath, err)
				}
				slog.Info("overlay crash recovery complete", "path", resolved)
				outcome.Restored = true
				return outcome, nil
			}
			slog.Warn("sentinel backup_path missing — overlay torn down but no original to restore",
				"path", resolved, "backup_path", s.BackupPath)
			return outcome, nil
		} else if errors.Is(vErr, ErrSentinelInvalid) {
			// Sentinel-shaped file we don't trust. Surface as
			// recovery-pending so the GUI can ask the user what to do.
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

	// Remove Data/ ONLY when we're about to restore the backup over it.
	// Removing an empty Data/ on a clean install (no FUSE mount, no backup)
	// would silently destroy the game's data dir — that bug surfaced in
	// TestRecoverIfNeeded_NoBackup. Gate the deletion on the backup
	// existing AND the current Data being safe to discard (empty after
	// unmount, OR was the FUSE-mounted dir we just unmounted).
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
				// Both Data/ (non-empty) and Data.orig/ exist — the daemon
				// can't safely guess which one to keep. Surface as
				// recovery-pending; the GUI prompts and (on confirm)
				// invokes RestoreFromBackup.
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

// RestoreFromBackup performs the destructive restore the user explicitly
// confirmed via the GUI prompt: rm -rf Data, mv Data.orig → Data. Refuses
// without a Data.orig sibling — calling it on an install with no backup
// is always a bug.
func RestoreFromBackup(dataPath string) error {
	resolved, err := filepath.Abs(dataPath)
	if err != nil {
		return fmt.Errorf("resolving %q: %w", dataPath, err)
	}
	backupPath := resolved + ".orig"
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

// unmountWithFallbacks tries fusermount3, then fusermount, then a lazy
// umount, returning the first success. Lazy umount (`umount -l`) is the last
// resort because it may leave processes still holding the mount confused; we
// only reach it when the user has neither fusermount tool installed.
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
