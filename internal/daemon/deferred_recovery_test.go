package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/vfs"
)

// deferredSessionFixture restarts a mounted farm with an injected running process and a fixed clock.
func deferredSessionFixture(t *testing.T) (*Daemon, *atomic.Bool, string, string) {
	t.Helper()
	_, games, install, dataPath := sessionFarmFixture(t)
	var running atomic.Bool
	running.Store(true)
	at := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	restarted := restartSessionDaemon(t, games, func() time.Time { return at }, func(string) (bool, error) {
		return running.Load(), nil
	})
	return restarted, &running, install, dataPath
}

// TestDeferredRecoveryGatesAllWriters checks that mod and dependency writers refuse deferred installs without modifying their files while profile-only edits remain available.
func TestDeferredRecoveryGatesAllWriters(t *testing.T) {
	cases := []struct {
		name      string
		operation string
		act       func(*testing.T, *Daemon, string, download.ArchiveSidecar) error
	}{
		{name: "rename", operation: dto.GameRunningOperationRename, act: func(_ *testing.T, d *Daemon, _ string, _ download.ArchiveSidecar) error {
			return d.RenameMod(depsGame, "SessionMod", "Renamed")
		}},
		{name: "uninstall", operation: dto.GameRunningOperationUninstall, act: func(_ *testing.T, d *Daemon, _ string, _ download.ArchiveSidecar) error {
			_, err := d.UninstallMod(depsGame, "SessionMod", true)
			return err
		}},
		{name: "import", operation: dto.BusyOperationImport, act: func(_ *testing.T, d *Daemon, _ string, _ download.ArchiveSidecar) error {
			_, err := d.ImportInstance(context.Background(), dto.ImportRequest{GameID: depsGame, ArchivePath: "missing"}, nil)
			return err
		}},
		{name: "register manual install", operation: "register_install", act: func(_ *testing.T, d *Daemon, _ string, _ download.ArchiveSidecar) error {
			_, err := d.RegisterManualInstall(depsGame, "SessionMod", "")
			return err
		}},
		{name: "dependency install", act: func(_ *testing.T, d *Daemon, archive string, _ download.ArchiveSidecar) error {
			d.svc.modDeps.installForRequests(depsGame, relFromDownloads(depsGame, archive), "", "install-1", map[string]bool{idKey(depUniqueID): true})
			return nil
		}},
		{name: "landed hook", act: func(_ *testing.T, d *Daemon, archive string, sidecar download.ArchiveSidecar) error {
			d.managerHooks().OnArchiveLanded(download.DownloadSnapshot{ID: "dl-1", GameID: depsGame}, archive, sidecar)
			d.background.wait()
			return nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, _, install, dataPath := deferredSessionFixture(t)
			archive, sidecar := writeLandedArchive(t, depNexusID, depFileID, "Dep Core", depArchiveFiles())
			if err := config.SaveGameSettings(depsGame, config.GameSettings{AutoInstall: true}); err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
			requests := &depRequestsDoc{SchemaVersion: 1, Batches: []depBatch{{BatchID: "one", Profile: "Default", CreatedAt: now, Entries: []depEntry{{UniqueID: depUniqueID, NexusModID: depNexusID, FileID: depFileID, DownloadID: "dl-1", State: depStateDownloading}}}}}
			if err := saveDependencyRequests(depsGame, requests); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(dependencyRequestsPath(depsGame))
			if err != nil {
				t.Fatal(err)
			}
			err = tc.act(t, d, archive, sidecar)
			if tc.operation != "" {
				requireDeferred(t, err, tc.operation)
			} else if err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(dependencyRequestsPath(depsGame))
			if err != nil || string(before) != string(after) {
				t.Fatalf("dependency requests changed: %v", err)
			}
			for _, path := range []string{
				filepath.Join(config.ModsDir(depsGame), "SessionMod", "Plain", "readme.txt"),
				filepath.Join(dataPath, vfs.SentinelFilename),
				filepath.Join(install, "session-root.txt"),
				archive,
			} {
				if _, err := os.Lstat(path); err != nil {
					t.Fatalf("writer changed %s: %v", path, err)
				}
			}
			for _, name := range []string{"Renamed", "Dep Core"} {
				if _, err := os.Lstat(filepath.Join(config.ModsDir(depsGame), name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("writer created %s: %v", name, err)
				}
			}
			if err := d.SetModList(depsGame, "Default", []dto.ModListEntryResult{{ModName: "SessionMod", Enabled: false}}); err != nil {
				t.Fatalf("profile-only edit during deferred recovery: %v", err)
			}
			if err := d.SetSeparators(depsGame, "Default", []dto.SeparatorResult{{Name: "Section"}}, true); err != nil {
				t.Fatalf("separator edit during deferred recovery: %v", err)
			}
			if err := d.SetPluginOrder(depsGame, "Default", []string{"Test.esp"}); err != nil {
				t.Fatalf("plugin order edit during deferred recovery: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(dataPath, vfs.SentinelFilename)); err != nil {
				t.Fatalf("profile-only edit changed the farm: %v", err)
			}
			d.mu.RLock()
			mm := d.mountMgrs[depsGame]
			d.mu.RUnlock()
			if err := mm.MarkDirty(nil); !errors.Is(err, vfs.ErrNotMounted) || mm.IsDirty() {
				t.Fatalf("MarkDirty on an unmounted manager = %v, dirty=%t", err, mm.IsDirty())
			}
		})
	}
}

// TestDeferredRecoveryCompletesAfterGameExits checks a retry restores the farm, publishes status and installs one held dependency landing exactly once.
func TestDeferredRecoveryCompletesAfterGameExits(t *testing.T) {
	d, running, install, dataPath := deferredSessionFixture(t)
	archive, sidecar := writeLandedArchive(t, depNexusID, depFileID, "Dep Core", depArchiveFiles())
	if err := config.SaveGameSettings(depsGame, config.GameSettings{AutoInstall: true}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	if err := saveDependencyRequests(depsGame, &depRequestsDoc{SchemaVersion: 1, Batches: []depBatch{{BatchID: "one", Profile: "Default", CreatedAt: at, Entries: []depEntry{{UniqueID: depUniqueID, NexusModID: depNexusID, FileID: depFileID, DownloadID: "dl-1", State: depStateDownloading}}}}}); err != nil {
		t.Fatal(err)
	}
	installs := subscribeInstalls(t, d)
	snap := download.DownloadSnapshot{ID: "dl-1", GameID: depsGame}
	d.svc.archives.handleLandedArchive(snap, archive, sidecar)
	d.svc.archives.handleLandedArchive(snap, archive, sidecar)
	if entry := onlyDepEntry(t, depUniqueID); entry.State != depStateDownloading {
		t.Fatalf("request consumed before recovery: %+v", entry)
	}
	running.Store(false)
	d.retryDeferredRecoveriesTick()
	if err := d.deferredFor(depsGame, "mount"); err != nil {
		t.Fatalf("install still deferred: %v", err)
	}
	for _, path := range []string{filepath.Join(dataPath, vfs.SentinelFilename), filepath.Join(install, "session-root.txt"), dataPath + ".orig"} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("recovery left %s: %v", path, err)
		}
	}
	statusDeadline := time.After(5 * time.Second)
	for {
		select {
		case evt := <-d.WatchStatus():
			if evt.VFSStatus != nil && evt.VFSStatus.GameID == depsGame {
				goto statusReceived
			}
		case <-statusDeadline:
			t.Fatal("no VFS status published after recovery")
		}
	}
statusReceived:
	if completed := waitCompleted(t, installs); completed.ModName != "Dep Core" {
		t.Fatalf("completed install = %+v", completed)
	}
	d.background.wait()
	if entry := onlyDepEntry(t, depUniqueID); entry.State != depStateEnablePending || entry.ModName != "Dep Core" {
		t.Fatalf("request after retry = %+v", entry)
	}
	if _, err := os.Stat(filepath.Join(config.ModsDir(depsGame), "Dep Core", "DepCore", "Mod.dll")); err != nil {
		t.Fatalf("held archive not installed: %v", err)
	}
	d.retryDeferredRecoveriesTick()
	for {
		select {
		case evt := <-installs:
			if evt.Completed != nil {
				t.Fatalf("archive installed twice: %+v", evt.Completed)
			}
		default:
			return
		}
	}
}

// TestRecoveryRetrySerializesWithLaunch checks a launch reservation holds the exclusive retry until released.
func TestRecoveryRetrySerializesWithLaunch(t *testing.T) {
	d, running, install, dataPath := deferredSessionFixture(t)
	running.Store(false)
	key := filepath.Clean(install)
	d.fenceMu.Lock()
	d.fenceShared = map[string]map[uint64]fenceHolder{key: {1: {op: dto.BusyOperationLaunch, gameID: depsGame}}}
	d.fenceMu.Unlock()
	err := d.RetryDeferredRecovery(depsGame)
	requireBusy(t, "retry with launch reservation", err, dto.BusyOperationLaunch)
	if _, err := os.Stat(filepath.Join(dataPath, vfs.SentinelFilename)); err != nil {
		t.Fatalf("retry changed the farm under a launch: %v", err)
	}
	d.releaseShared(key, 1)
	if err := d.VFSService.RetryDeferredRecovery(depsGame); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dataPath, vfs.SentinelFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retry did not restore the farm: %v", err)
	}
}

// TestConcurrentRetryKeepsTheRecoveryFence checks an overlapping retry cannot enter recovery and a completed retry is a no-op.
func TestConcurrentRetryKeepsTheRecoveryFence(t *testing.T) {
	d, running, _, dataPath := deferredSessionFixture(t)
	running.Store(false)
	entered := make(chan struct{})
	continueScan := make(chan struct{})
	d.procScan = func(string) (bool, error) {
		close(entered)
		<-continueScan
		return false, nil
	}
	finished := make(chan error, 1)
	go func() { finished <- d.RetryDeferredRecovery(depsGame) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first retry did not enter the process scan")
	}
	requireBusy(t, "overlapping recovery", d.RetryDeferredRecovery(depsGame), "recovery")
	close(continueScan)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if err := d.RetryDeferredRecovery(depsGame); err != nil {
		t.Fatalf("repeat recovery: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dataPath, vfs.SentinelFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovery did not restore Data: %v", err)
	}
}

// TestDeferredRecoveryDoesNotBlockOtherGames checks startup and retry leave another install free to recover and mount.
func TestDeferredRecoveryDoesNotBlockOtherGames(t *testing.T) {
	d, games, stardewInstall, stardewData := sessionFarmFixture(t)
	other := newSkyrimGames(t)
	for id, gc := range other {
		games[id] = gc
		d.mu.Lock()
		d.config.Games[id] = gc
		d.ensureMountManager(id, gc)
		d.mu.Unlock()
	}
	if _, err := d.MountVFS("skyrimse", "Default"); err != nil {
		t.Fatal(err)
	}
	otherData := filepath.Join(other["skyrimse"].InstallPath, "Data")
	var running atomic.Bool
	running.Store(true)
	restarted, err := newWithClock(configWithGames(games), time.Now, func(root string) (bool, error) {
		return root == stardewInstall && running.Load(), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restarted.Shutdown)
	restarted.RecoverAll()
	if _, err := os.Stat(filepath.Join(stardewData, vfs.SentinelFilename)); err != nil {
		t.Fatalf("deferred farm changed: %v", err)
	}
	if err := restarted.deferredFor("skyrimse", "mount"); err != nil {
		t.Fatalf("unrelated install deferred: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(otherData, vfs.SentinelFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("other install did not recover its farm: %v", err)
	}
	if _, err := os.Stat(filepath.Join(otherData, "Skyrim.esm")); err != nil {
		t.Fatalf("other install lost its original Data: %v", err)
	}
	if _, err := restarted.MountVFS("skyrimse", "Default"); err != nil {
		t.Fatalf("unrelated install cannot mount: %v", err)
	}
	if err := restarted.UnmountVFS("skyrimse"); err != nil {
		t.Fatalf("unrelated install cannot unmount: %v", err)
	}
}

// configWithGames creates a fresh config from a test's isolated game installations.
func configWithGames(games map[string]config.GameConfig) *config.Config {
	cfg := config.DefaultConfig()
	for id, gc := range games {
		cfg.Games[id] = gc
	}
	return cfg
}

// TestRetryWaitsForLaunchTicketGrace checks a fresh ticket defers recovery until the injected clock passes its launch grace.
func TestRetryWaitsForLaunchTicketGrace(t *testing.T) {
	original, games, _, dataPath := sessionFarmFixture(t)
	clock := &depClock{now: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)}
	original.now = clock.Now
	if err := original.writeLaunchTicketForGame(depsGame, "Default", dataPath); err != nil {
		t.Fatal(err)
	}
	d := restartSessionDaemon(t, games, clock.Now, func(string) (bool, error) { return false, nil })
	requireDeferred(t, d.RetryDeferredRecovery(depsGame), "recovery")
	clock.Advance(steamLaunchGrace)
	if err := d.RetryDeferredRecovery(depsGame); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(dataPath + sessionTicketSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("launch ticket survived restoration: %v", err)
	}
	if err := d.RetryDeferredRecovery(depsGame); err != nil {
		t.Fatalf("retry on an already recovered install: %v", err)
	}
}

// TestDeferredRetryPublishesAmbiguousRecovery checks an unrecoverable farm becomes pending and still publishes a status after deferred recovery is lifted.
func TestDeferredRetryPublishesAmbiguousRecovery(t *testing.T) {
	d, running, _, dataPath := deferredSessionFixture(t)
	if err := os.RemoveAll(dataPath + ".orig"); err != nil {
		t.Fatal(err)
	}
	running.Store(false)
	if err := d.RetryDeferredRecovery(depsGame); err != nil {
		t.Fatal(err)
	}
	if err := d.deferredFor(depsGame, "mount"); err != nil {
		t.Fatalf("install still deferred: %v", err)
	}
	d.mu.RLock()
	recoveryPending := d.recoveryPendingFor(depsGame)
	d.mu.RUnlock()
	if recoveryPending == nil {
		t.Fatal("ambiguous Data recovery was not recorded")
	}
	var status, pending bool
	deadline := time.After(5 * time.Second)
	for !status || !pending {
		select {
		case evt := <-d.WatchStatus():
			status = status || evt.VFSStatus != nil && evt.VFSStatus.GameID == depsGame
			pending = pending || evt.RecoveryPending != nil && evt.RecoveryPending.GameID == depsGame
		case <-deadline:
			t.Fatalf("recovery events: status=%t pending=%t", status, pending)
		}
	}
}

// TestDeferredLandingWaitsForManualRecovery checks held downloads do not consume dependency requests until ambiguous recovery is confirmed.
func TestDeferredLandingWaitsForManualRecovery(t *testing.T) {
	d, running, _, dataPath := deferredSessionFixture(t)
	archive, sidecar := writeLandedArchive(t, depNexusID, depFileID, "Dep Core", depArchiveFiles())
	if err := config.SaveGameSettings(depsGame, config.GameSettings{AutoInstall: true}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	if err := saveDependencyRequests(depsGame, &depRequestsDoc{SchemaVersion: 1, Batches: []depBatch{{BatchID: "one", Profile: "Default", CreatedAt: at, Entries: []depEntry{{UniqueID: depUniqueID, NexusModID: depNexusID, FileID: depFileID, DownloadID: "dl-1", State: depStateDownloading}}}}}); err != nil {
		t.Fatal(err)
	}
	installs := subscribeInstalls(t, d)
	d.svc.archives.handleLandedArchive(download.DownloadSnapshot{ID: "dl-1", GameID: depsGame}, archive, sidecar)
	if err := os.Remove(filepath.Join(dataPath, vfs.SentinelFilename)); err != nil {
		t.Fatal(err)
	}
	running.Store(false)
	if err := d.RetryDeferredRecovery(depsGame); err != nil {
		t.Fatal(err)
	}
	if pending := onlyDepEntry(t, depUniqueID); pending.State != depStateDownloading {
		t.Fatalf("request consumed before manual recovery: %+v", pending)
	}
	if _, err := os.Lstat(filepath.Join(config.ModsDir(depsGame), "Dep Core")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archive installed before manual recovery: %v", err)
	}
	if err := d.RestoreFromBackup(depsGame); err != nil {
		t.Fatal(err)
	}
	if completed := waitCompleted(t, installs); completed.ModName != "Dep Core" {
		t.Fatalf("completed install = %+v", completed)
	}
	d.background.wait()
	if entry := onlyDepEntry(t, depUniqueID); entry.State != depStateEnablePending {
		t.Fatalf("request after manual recovery: %+v", entry)
	}
}

// TestExplicitRetryRefusesWhileRunning checks process and scan failures keep recovery deferred without changing the farm.
func TestExplicitRetryRefusesWhileRunning(t *testing.T) {
	for _, tc := range []struct {
		name string
		scan func(string) (bool, error)
	}{
		{name: "game running", scan: func(string) (bool, error) { return true, nil }},
		{name: "scan failed", scan: func(string) (bool, error) { return false, errors.New("unreadable process table") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, games, _, dataPath := sessionFarmFixture(t)
			at := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
			d := restartSessionDaemon(t, games, func() time.Time { return at }, tc.scan)
			requireDeferred(t, d.RetryDeferredRecovery(depsGame), "recovery")
			if _, err := os.Stat(filepath.Join(dataPath, vfs.SentinelFilename)); err != nil {
				t.Fatalf("retry changed the running farm: %v", err)
			}
		})
	}
}
