package daemon

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/smapi"
	"github.com/parka/gorganizer/internal/vfs"
)

// isolatedRecoveryDaemon starts recovery with temporary state and a process table that has no running game.
func isolatedRecoveryDaemon(t *testing.T, install string) *Daemon {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	isolateDaemonState(t)
	cfg := config.DefaultConfig()
	cfg.Games["stardewvalley"] = newStardewGames(install)["stardewvalley"]
	d, err := newWithClock(cfg, time.Now, func(string) (bool, error) { return false, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Shutdown)
	d.RecoverAll()
	return d
}

// ambiguousDataFixture creates an unrecognized deploy folder alongside its intact backup.
func ambiguousDataFixture(t *testing.T) (string, string) {
	t.Helper()
	install := newStardewInstall(t)
	data := filepath.Join(install, "Mods")
	writeFileContent(t, filepath.Join(data, "live.txt"), "live")
	writeFileContent(t, filepath.Join(install, "Mods.orig", "backup.txt"), "backup")
	return install, data
}

// TestRecoveryPendingCarriesKindAndID checks every recovery producer assigns a typed random identity and keeps it on republication.
func TestRecoveryPendingCarriesKindAndID(t *testing.T) {
	for _, tc := range []struct {
		name    string
		kind    dto.RecoveryKind
		prepare func(*testing.T) string
	}{
		{"data", dto.RecoveryKindData, func(t *testing.T) string { install, _ := ambiguousDataFixture(t); return install }},
		{"loader", dto.RecoveryKindModLoader, func(t *testing.T) string {
			install := newStardewInstall(t)
			writeFixture(t, filepath.Join(install, "StardewModdingAPI"))
			writeLoaderIntent(t, install, "StardewModdingAPI", smapi.FileID{Dev: 1, Ino: 1})
			return install
		}},
		{"root", dto.RecoveryKindGameRoot, func(t *testing.T) string {
			install := newStardewInstall(t)
			writeFixture(t, filepath.Join(install, vfs.RootBackupDirName, "leftover"))
			return install
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := isolatedRecoveryDaemon(t, tc.prepare(t))
			d.mu.RLock()
			pending := d.recoveryPendingFor("stardewvalley")
			d.mu.RUnlock()
			if pending == nil || pending.Kind != tc.kind {
				t.Fatalf("pending = %+v, want kind %v", pending, tc.kind)
			}
			decoded, err := hex.DecodeString(pending.RecoveryID)
			if err != nil || len(pending.RecoveryID) != 32 || len(decoded) != 16 {
				t.Fatalf("recovery ID = %q (%v), want 32 hex characters", pending.RecoveryID, err)
			}
			d.RecoverAll()
			d.mu.RLock()
			again := d.recoveryPendingFor("stardewvalley")
			d.mu.RUnlock()
			if again == nil || again.RecoveryID != pending.RecoveryID {
				t.Fatalf("republished item = %+v, want identity %q", again, pending.RecoveryID)
			}
			replacement := *pending
			replacement.Reason += " (new drift)"
			replaced := identifiedRecovery(pending, &replacement)
			if replaced.RecoveryID == pending.RecoveryID || replaced.RecoveryID == "" {
				t.Fatalf("replacement retained identity %q", replaced.RecoveryID)
			}
		})
	}
}

// TestStaleRecoveryConfirmationRefused checks a loader prompt cannot consent to a different Data restoration.
func TestStaleRecoveryConfirmationRefused(t *testing.T) {
	install, data := ambiguousDataFixture(t)
	d := isolatedRecoveryDaemon(t, install)
	d.registerLoaderPending([]string{"stardewvalley"}, install, errors.New("interrupted"), d.publishRecoveryEvent)
	d.pendingRecoveriesMu.Lock()
	loaderID := d.loaderPendingRecoveries["stardewvalley"].RecoveryID
	delete(d.loaderPendingRecoveries, "stardewvalley")
	dataID := d.pendingRecoveries[data].RecoveryID
	d.pendingRecoveriesMu.Unlock()
	var stale *dto.RecoveryStaleError
	if err := d.RestoreFromBackup("stardewvalley", dto.RecoveryKindModLoader, loaderID); !errors.As(err, &stale) {
		t.Fatalf("stale confirmation = %v, want RecoveryStaleError", err)
	}
	for path, want := range map[string]string{
		filepath.Join(data, "live.txt"):                   "live",
		filepath.Join(install, "Mods.orig", "backup.txt"): "backup",
	} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Errorf("%s = %q (%v), want %q", path, got, err, want)
		}
	}
	count := 0
	deadline := time.After(5 * time.Second)
	for count < 2 {
		select {
		case evt := <-d.WatchStatus():
			if evt.RecoveryPending != nil && evt.RecoveryPending.RecoveryID == dataID {
				count++
			}
		case <-deadline:
			t.Fatalf("Data recovery announced %d times, want startup and stale re-announcement", count)
		}
	}
	if err := d.RestoreFromBackup("stardewvalley", dto.RecoveryKindData, "incorrect"); !errors.As(err, &stale) {
		t.Fatalf("wrong ID = %v, want RecoveryStaleError", err)
	}
	if err := d.RestoreFromBackup("stardewvalley", dto.RecoveryKind(-1), ""); !errors.As(err, &stale) {
		t.Fatalf("unknown kind = %v, want RecoveryStaleError", err)
	}
	if err := d.RestoreFromBackup("stardewvalley", dto.RecoveryKindData, dataID); err != nil {
		t.Fatalf("matching Data confirmation: %v", err)
	}
	if err := d.RestoreFromBackup("stardewvalley", dto.RecoveryKindData, dataID); !errors.As(err, &stale) {
		t.Fatalf("resolved item confirmation = %v, want RecoveryStaleError", err)
	}
}

// TestLegacyRestoreConfirmationStillWorks checks an unset kind and empty identity restore the current Data backup.
func TestLegacyRestoreConfirmationStillWorks(t *testing.T) {
	install, data := ambiguousDataFixture(t)
	d := isolatedRecoveryDaemon(t, install)
	if err := d.RestoreFromBackup("stardewvalley", dto.RecoveryKindUnspecified, ""); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(filepath.Join(data, "backup.txt")); err != nil || string(content) != "backup" {
		t.Fatalf("restored backup = %q (%v)", content, err)
	}
	if _, err := os.Lstat(filepath.Join(data, "live.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unrecognized Data survived restoration: %v", err)
	}
}

// waitForLifecycle waits for a streamed status of the requested game and lifecycle state.
func waitForLifecycle(t *testing.T, d *Daemon, want dto.VFSLifecycleState) *dto.VFSStatusResult {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case evt := <-d.WatchStatus():
			if evt.VFSStatus != nil && evt.VFSStatus.GameID == depsGame && evt.VFSStatus.LifecycleState == want {
				return evt.VFSStatus
			}
		case <-deadline:
			t.Fatalf("no lifecycle status %v on the stream", want)
		}
	}
}

// TestDeferredStatusSnapshotMatchesStream checks deferral and recovery updates agree between status snapshots and the stream.
func TestDeferredStatusSnapshotMatchesStream(t *testing.T) {
	d, running, _, _ := deferredSessionFixture(t)
	before, err := d.GetVFSStatus(depsGame)
	if err != nil {
		t.Fatal(err)
	}
	if before.LifecycleState != dto.VFSLifecycleStateRecoveryDeferred || before.LifecycleReason != "game_running" {
		t.Fatalf("deferred snapshot = %+v", before)
	}
	streamed := waitForLifecycle(t, d, dto.VFSLifecycleStateRecoveryDeferred)
	if streamed.LifecycleReason != before.LifecycleReason {
		t.Fatalf("deferred stream = %+v, snapshot = %+v", streamed, before)
	}
	running.Store(false)
	if err := d.RetryDeferredRecovery(depsGame); err != nil {
		t.Fatal(err)
	}
	after, err := d.GetVFSStatus(depsGame)
	if err != nil {
		t.Fatal(err)
	}
	streamed = waitForLifecycle(t, d, dto.VFSLifecycleStateReady)
	if after.LifecycleState != dto.VFSLifecycleStateReady || after.LifecycleReason != "" || streamed.LifecycleState != after.LifecycleState || streamed.LifecycleReason != after.LifecycleReason {
		t.Fatalf("recovered snapshot = %+v, stream = %+v", after, streamed)
	}
	d.mu.Lock()
	delete(d.mountMgrs, depsGame)
	d.mu.Unlock()
	withoutManager, err := d.GetVFSStatus(depsGame)
	if err != nil || withoutManager.LifecycleState != dto.VFSLifecycleStateReady {
		t.Fatalf("status without manager = %+v (%v)", withoutManager, err)
	}
}

// TestRecoveryLifecycleReasonsWithoutMountManager checks that deferred causes remain visible when the install has no mount manager.
func TestRecoveryLifecycleReasonsWithoutMountManager(t *testing.T) {
	install := newStardewInstall(t)
	d := isolatedRecoveryDaemon(t, install)
	d.mu.Lock()
	delete(d.mountMgrs, "stardewvalley")
	d.mu.Unlock()
	for _, tc := range []struct {
		reason string
		want   string
	}{
		{"a game process may still be running", "game_running"},
		{"the game was launched less than two minutes ago", "launch_grace"},
		{"the game process check failed: unreadable", "process_scan_failed"},
		{"the game launch record could not be read: permission denied", "launch_record_unreadable"},
		{"the game launch record is invalid", "launch_record_invalid"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			d.pendingRecoveriesMu.Lock()
			d.deferredRecoveries[filepath.Clean(install)] = deferredRecovery{reason: tc.reason}
			d.pendingRecoveriesMu.Unlock()
			status, err := d.GetVFSStatus("stardewvalley")
			if err != nil || status.LifecycleState != dto.VFSLifecycleStateRecoveryDeferred || status.LifecycleReason != tc.want {
				t.Fatalf("status = %+v (%v), want deferred %s", status, err, tc.want)
			}
		})
	}
}

// TestRetryRecoveryCannotForceRunningGame checks an explicit retry neither changes a running farm nor clears its deferred state.
func TestRetryRecoveryCannotForceRunningGame(t *testing.T) {
	d, _, _, data := deferredSessionFixture(t)
	var deferred *dto.RecoveryDeferredError
	if err := d.RetryDeferredRecovery(depsGame); !errors.As(err, &deferred) {
		t.Fatalf("running retry = %v, want RecoveryDeferredError", err)
	}
	if _, err := os.Stat(filepath.Join(data, vfs.SentinelFilename)); err != nil {
		t.Fatalf("running farm changed: %v", err)
	}
	status, err := d.GetVFSStatus(depsGame)
	if err != nil || status.LifecycleState != dto.VFSLifecycleStateRecoveryDeferred {
		t.Fatalf("running status = %+v (%v)", status, err)
	}
	if err := d.RetryDeferredRecovery("unknown-game"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unknown game retry = %v, want not found", err)
	}
}
