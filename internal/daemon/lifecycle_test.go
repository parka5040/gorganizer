package daemon

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/dto"
)

// requireShuttingDown fails unless err is a ShuttingDownError.
func requireShuttingDown(t *testing.T, what string, err error) {
	t.Helper()
	var shuttingDown *dto.ShuttingDownError
	if !errors.As(err, &shuttingDown) {
		t.Errorf("%s during shutdown = %v, want ShuttingDownError", what, err)
	}
}

// TestShutdownReleasesTheDaemonLockBeforeStoppingIPC locks that a handler waiting on the daemon lock while shutdown deactivates a farm can finish while the IPC server drains.
func TestShutdownReleasesTheDaemonLockBeforeStoppingIPC(t *testing.T) {
	d, _ := newLoaderTestDaemon(t, nil, nil)
	if _, err := d.MountVFS("stardewvalley", "Default"); err != nil {
		t.Fatalf("MountVFS: %v", err)
	}
	var handlerFinished atomic.Bool
	stopIPC := func() {
		done := make(chan struct{})
		go func() {
			defer close(done)
			if _, err := d.GetVFSStatus("stardewvalley"); err != nil {
				t.Errorf("GetVFSStatus during shutdown: %v", err)
			}
		}()
		select {
		case <-done:
			handlerFinished.Store(true)
		case <-time.After(2 * time.Second):
		}
	}
	stopped := make(chan struct{})
	go func() {
		d.shutdownAll(stopIPC)
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(15 * time.Second):
		t.Fatal("shutdown did not complete with an in-flight GetVFSStatus")
	}
	if !handlerFinished.Load() {
		t.Fatal("GetVFSStatus stayed blocked on the daemon lock while the IPC server stopped")
	}
	d.mu.RLock()
	mounted := d.mountMgrs["stardewvalley"].IsMounted()
	d.mu.RUnlock()
	if mounted {
		t.Error("shutdown left an idle farm mounted")
	}
}

// TestShutdownRefusesNewWorkWithATypedError locks that fenced and installing entry points refuse fast once shutdown began.
func TestShutdownRefusesNewWorkWithATypedError(t *testing.T) {
	d, install := newLoaderTestDaemon(t, &fakeLoaderEngine{}, &fakeLoaderArtifacts{})
	if _, err := d.MountVFS("stardewvalley", "Default"); err != nil {
		t.Fatalf("MountVFS: %v", err)
	}
	d.beginShutdown()
	t.Cleanup(func() {
		d.shuttingDown.Store(false)
		_ = d.UnmountVFS("stardewvalley")
	})

	requireShuttingDown(t, "RebuildVFS", d.RebuildVFS("stardewvalley"))
	requireShuttingDown(t, "UnmountVFS", d.UnmountVFS("stardewvalley"))
	_, err := d.MountVFS("stardewvalley", "Other")
	requireShuttingDown(t, "MountVFS", err)
	_, err = d.LaunchGame("stardewvalley", false, "Default")
	requireShuttingDown(t, "LaunchGame", err)
	requireShuttingDown(t, "ConfigureGame", d.ConfigureGame("stardewvalley", "Stardew Valley", 413150, install, "Mods"))
	_, err = d.InstallModLoader(context.Background(), "stardewvalley", false)
	requireShuttingDown(t, "InstallModLoader", err)
	_, _, err = d.StartInstall(dto.StartInstallRequest{GameID: "stardewvalley", ArchiveRelPath: "a.zip", Mode: dto.InstallAsNewMod})
	requireShuttingDown(t, "StartInstall", err)
	_, _, _, err = d.ReinstallMod("stardewvalley", "Anything")
	requireShuttingDown(t, "ReinstallMod", err)
	_, err = d.ImportInstance(context.Background(), dto.ImportRequest{GameID: "stardewvalley", ArchivePath: "missing.tar.zst"}, func(dto.TransferProgress) {})
	requireShuttingDown(t, "ImportInstance", err)
	_, err = d.UninstallMod("stardewvalley", "Anything", false)
	requireShuttingDown(t, "UninstallMod", err)
	requireShuttingDown(t, "RenameMod", d.RenameMod("stardewvalley", "Anything", "Other"))
	_, err = d.RegisterManualInstall("stardewvalley", "Anything", "")
	requireShuttingDown(t, "RegisterManualInstall", err)
	requireShuttingDown(t, "RestoreFromBackup", d.RestoreFromBackup("stardewvalley", dto.RecoveryKindUnspecified, ""))
	_, err = d.InstallTTWPrereqs()
	requireShuttingDown(t, "InstallTTWPrereqs", err)
	_, err = d.LaunchTTWInstaller(dto.TTWInstallerInfoResult{}, "TTW Data")
	requireShuttingDown(t, "LaunchTTWInstaller", err)

	d.mu.RLock()
	mounted := d.mountMgrs["stardewvalley"].IsMounted()
	d.mu.RUnlock()
	if !mounted {
		t.Fatal("a refused operation changed the farm")
	}
}

// TestShutdownWaitsForBackgroundInstallsAndRefusesNewOnes locks that shutdown waits for tracked landing work within its bound and starts none afterwards.
func TestShutdownWaitsForBackgroundInstallsAndRefusesNewOnes(t *testing.T) {
	d, _ := newLoaderTestDaemon(t, nil, nil)
	var finished atomic.Bool
	if !d.goBackground("slow landing", func() {
		time.Sleep(300 * time.Millisecond)
		finished.Store(true)
	}) {
		t.Fatal("background work refused before shutdown")
	}
	d.shutdownAll(nil)
	if !finished.Load() {
		t.Fatal("shutdown returned before the background install finished")
	}
	if d.goBackground("late landing", func() { t.Error("background work ran after shutdown") }) {
		t.Fatal("background work accepted after shutdown")
	}
}

// TestShutdownLaunchWaitUsesTheRemainingBudget locks that the launch wait ends at a deadline measured from the start of shutdown, not after the earlier waits.
func TestShutdownLaunchWaitUsesTheRemainingBudget(t *testing.T) {
	d, _ := newLoaderTestDaemon(t, nil, nil)
	previous := shutdownLaunchDeadline
	shutdownLaunchDeadline = 1500 * time.Millisecond
	t.Cleanup(func() { shutdownLaunchDeadline = previous })
	never := make(chan struct{})
	t.Cleanup(func() { close(never) })
	d.launchedMu.Lock()
	d.launched[4242] = &launchedGame{gameID: "stardewvalley", done: never}
	d.launchedMu.Unlock()
	d.goBackground("slow landing", func() { time.Sleep(time.Second) })

	begin := time.Now()
	d.shutdownAll(nil)
	elapsed := time.Since(begin)
	if elapsed < shutdownLaunchDeadline {
		t.Fatalf("shutdown took %v, returning before the launch deadline %v", elapsed, shutdownLaunchDeadline)
	}
	if elapsed > shutdownLaunchDeadline+700*time.Millisecond {
		t.Fatalf("shutdown took %v; the launch wait did not end at %v after shutdown began", elapsed, shutdownLaunchDeadline)
	}
}

// TestStatusPublishesRacingCloseNeverPanic locks that every status send is guarded against the stream closing concurrently.
func TestStatusPublishesRacingCloseNeverPanic(t *testing.T) {
	for round := 0; round < 20; round++ {
		d, _ := newLoaderTestDaemon(t, nil, nil)
		var wg sync.WaitGroup
		start := make(chan struct{})
		senders := []func(int){
			func(i int) { d.emitInfo(fmt.Sprintf("info %d", i)) },
			func(i int) { d.publishStatus(dto.StatusEventResult{Info: fmt.Sprintf("plugin %d", i)}) },
			func(i int) { d.publishGuarded(dto.StatusEventResult{Info: fmt.Sprintf("guarded %d", i)}) },
		}
		for _, send := range senders {
			wg.Add(1)
			go func(send func(int)) {
				defer wg.Done()
				<-start
				for i := 0; i < 200; i++ {
					send(i)
				}
			}(send)
		}
		close(start)
		d.closeStatus()
		wg.Wait()
	}
}

// TestStatusDrainNeverBlocksShutdownWithoutAWatcher locks that closing the status stream completes although nobody reads the coalesced channel.
func TestStatusDrainNeverBlocksShutdownWithoutAWatcher(t *testing.T) {
	d, _ := newLoaderTestDaemon(t, nil, nil)
	for i := 0; i < 200; i++ {
		d.emitInfo(fmt.Sprintf("line %d", i))
		if i%32 == 0 {
			time.Sleep(time.Millisecond)
		}
	}
	d.closeStatus()
	select {
	case <-d.coalescerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the status drain blocked on an unread channel after the stream closed")
	}
}
