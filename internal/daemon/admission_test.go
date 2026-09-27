package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/vfs"
)

// TestAdmissionBlocksUnmountAndApply keeps a mounted farm intact until a Steam launch has been tracked.
func TestAdmissionBlocksUnmountAndApply(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d, install := newLoaderTestDaemon(t, nil, nil)
	fakeProcesses(d, false, nil)
	writeFileContent(t, filepath.Join(config.ModsDir("stardewvalley"), "Plain", "Plain", "readme.txt"), "mod file")
	setStardewModList(t, d, "Default", map[string]bool{"Plain": true})
	if _, err := d.MountVFS("stardewvalley", "Default"); err != nil {
		t.Fatal(err)
	}
	dataFile := filepath.Join(install, "Mods", "Plain", "readme.txt")
	before, err := os.ReadFile(dataFile)
	if err != nil {
		t.Fatal(err)
	}
	admitted := make(chan struct{})
	continueLaunch := make(chan struct{})
	d.launchFault = func(step string) error {
		if step == "admitted" {
			close(admitted)
			<-continueLaunch
		}
		return nil
	}
	recorder := &steamOpenRecorder{}
	d.steamOpener = recorder.open
	launchDone := make(chan error, 1)
	go func() {
		_, err := d.LaunchGame("stardewvalley", false, "Default")
		launchDone <- err
	}()
	released, finished := false, false
	defer func() {
		if !released {
			close(continueLaunch)
		}
		if !finished {
			<-launchDone
		}
	}()
	<-admitted
	unmountDone := make(chan error, 1)
	applyDone := make(chan error, 1)
	go func() { unmountDone <- d.UnmountVFS("stardewvalley") }()
	go func() { applyDone <- d.RebuildVFS("stardewvalley") }()
	requireBusyHolder(t, "unmount during admission", <-unmountDone, "stardewvalley", dto.BusyOperationLaunch, "stardewvalley")
	requireBusyHolder(t, "Apply during admission", <-applyDone, "stardewvalley", dto.BusyOperationLaunch, "stardewvalley")
	if contents, err := os.ReadFile(dataFile); err != nil || string(contents) != string(before) {
		t.Fatalf("mounted file after refused operations = %q, %v", contents, err)
	}
	if _, err := os.Stat(filepath.Join(install, "Mods", vfs.SentinelFilename)); err != nil {
		t.Fatalf("mounted sentinel after refused operations: %v", err)
	}
	if recorder.count() != 0 {
		t.Fatal("Steam opened before the launch was released")
	}
	close(continueLaunch)
	released = true
	launchErr := <-launchDone
	finished = true
	if launchErr != nil {
		t.Fatalf("LaunchGame: %v", launchErr)
	}
	requireGameRunning(t, "unmount after Steam launch", d.UnmountVFS("stardewvalley"), dto.GameRunningOperationUnmount)
	requireGameRunning(t, "Apply after Steam launch", d.RebuildVFS("stardewvalley"), dto.GameRunningOperationApply)
	if recorder.count() != 1 {
		t.Fatalf("Steam opened %d times, want once", recorder.count())
	}
}

// TestOwnedPreparationExcludesOnlyItsReservation applies a launch's changes but refuses another outstanding admission.
func TestOwnedPreparationExcludesOnlyItsReservation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d, recorder, now := newDirtyLaunchDaemon(t)
	*now = now.Add(steamLaunchGrace + time.Minute)
	if _, err := d.LaunchGame("stardewvalley", false, "Default"); err != nil {
		t.Fatalf("launch with pending changes: %v", err)
	}
	if d.mountMgrs["stardewvalley"].IsDirty() || recorder.count() != 1 {
		t.Fatalf("launch did not apply its own pending changes: dirty=%v, opens=%d", d.mountMgrs["stardewvalley"].IsDirty(), recorder.count())
	}
	d.setSteamLaunched("stardewvalley", false)
	first, err := d.acquireSharedOwned("stardewvalley", dto.BusyOperationLaunch)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	second, err := d.acquireSharedOwned("stardewvalley", dto.BusyOperationTool)
	if err != nil {
		t.Fatal(err)
	}
	requireBusyHolder(t, "owned Apply with a second admission", d.svc.vfs.rebuildVFSOwned("stardewvalley", first.id), "stardewvalley", dto.BusyOperationTool, "stardewvalley")
	requireBusyHolder(t, "public Apply with both admissions", d.RebuildVFS("stardewvalley"), "stardewvalley", dto.BusyOperationLaunch, "stardewvalley")
	second.Release()
	if err := d.svc.vfs.rebuildVFSOwned("stardewvalley", first.id); err != nil {
		t.Fatalf("Apply with only its own admission: %v", err)
	}
}

// TestLaunchOwnAdmissionAllowsAutoMount mounts the farm before recording a Steam launch.
func TestLaunchOwnAdmissionAllowsAutoMount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d, _ := newLoaderTestDaemon(t, nil, nil)
	fakeProcesses(d, false, nil)
	recorder := &steamOpenRecorder{}
	d.steamOpener = recorder.open
	if _, err := d.LaunchGame("stardewvalley", false, "Default"); err != nil {
		t.Fatalf("LaunchGame: %v", err)
	}
	if !d.mountMgrs["stardewvalley"].IsMounted() || recorder.count() != 1 {
		t.Fatalf("launch did not auto-mount and open Steam once: mounted=%v, opens=%d", d.mountMgrs["stardewvalley"].IsMounted(), recorder.count())
	}
}

// TestUnmountRefusesRunningGameAndGrace checks process detection, launch grace, stale flags, and scan failures.
func TestUnmountRefusesRunningGameAndGrace(t *testing.T) {
	for _, tc := range []struct {
		name       string
		running    bool
		scanErr    error
		flag       bool
		elapsed    time.Duration
		wantRefuse bool
	}{
		{name: "running process", running: true, wantRefuse: true},
		{name: "fresh launch", flag: true, elapsed: steamLaunchGrace / 2, wantRefuse: true},
		{name: "old launch", flag: true, elapsed: steamLaunchGrace + time.Second},
		{name: "scan error", scanErr: errors.New("process list unavailable"), flag: true, elapsed: steamLaunchGrace + time.Second, wantRefuse: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			d, install := newLoaderTestDaemon(t, nil, nil)
			fakeProcesses(d, tc.running, tc.scanErr)
			if _, err := d.MountVFS("stardewvalley", "Default"); err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
			d.mu.Lock()
			d.now = func() time.Time { return now }
			d.mu.Unlock()
			if tc.flag {
				d.setSteamLaunched("stardewvalley", true)
			}
			now = now.Add(tc.elapsed)
			err := d.UnmountVFS("stardewvalley")
			if tc.wantRefuse {
				requireGameRunning(t, tc.name, err, dto.GameRunningOperationUnmount)
				if !d.mountMgrs["stardewvalley"].IsMounted() {
					t.Fatal("unmount removed the farm while the game might be running")
				}
				if tc.flag {
					d.launchedMu.Lock()
					flagged := d.steamLaunched["stardewvalley"]
					d.launchedMu.Unlock()
					if !flagged {
						t.Fatal("refused unmount cleared the Steam launch flag")
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("UnmountVFS: %v", err)
			}
			if d.mountMgrs["stardewvalley"].IsMounted() {
				t.Fatal("successful unmount left the farm mounted")
			}
			d.launchedMu.Lock()
			flagged := d.steamLaunched["stardewvalley"]
			d.launchedMu.Unlock()
			if flagged {
				t.Fatal("successful unmount kept the old Steam launch flag")
			}
			if _, err := os.Stat(filepath.Join(install, "Mods", vfs.SentinelFilename)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("sentinel after successful unmount: %v", err)
			}
		})
	}
}

// TestUnmountPreservesFlagOnDeactivationFailure keeps the launch flag if restoring vanilla Data fails.
func TestUnmountPreservesFlagOnDeactivationFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d, install := newLoaderTestDaemon(t, nil, nil)
	fakeProcesses(d, false, nil)
	if _, err := d.MountVFS("stardewvalley", "Default"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	d.mu.Lock()
	d.now = func() time.Time { return now }
	d.mu.Unlock()
	d.setSteamLaunched("stardewvalley", true)
	now = now.Add(steamLaunchGrace + time.Second)
	if err := os.WriteFile(filepath.Join(install, "Mods", vfs.SentinelFilename), []byte("invalid sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.UnmountVFS("stardewvalley"); err == nil {
		t.Fatal("unmount with an invalid sentinel succeeded")
	}
	d.launchedMu.Lock()
	flagged := d.steamLaunched["stardewvalley"]
	d.launchedMu.Unlock()
	if !flagged || !d.mountMgrs["stardewvalley"].IsMounted() {
		t.Fatal("failed unmount cleared the launch flag or tore down the farm")
	}
}

// TestSharedInstallAdmissionBlocksSwapAndApply checks that TTW and Fallout New Vegas share admission protection.
func TestSharedInstallAdmissionBlocksSwapAndApply(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := newFNVAndTTWDaemon(t)
	fakeProcesses(d, false, nil)
	if _, err := d.MountVFS("falloutnv", "Default"); err != nil {
		t.Fatal(err)
	}
	reservation, err := d.acquireSharedOwned("ttw", dto.BusyOperationLaunch)
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.Release()
	requireBusyHolder(t, "FNV Apply while TTW launches", d.RebuildVFS("falloutnv"), "falloutnv", dto.BusyOperationLaunch, "ttw")
	requireBusyHolder(t, "FNV unmount while TTW launches", d.UnmountVFS("falloutnv"), "falloutnv", dto.BusyOperationLaunch, "ttw")
	_, err = d.MountVFSWithSwap("ttw", "Default")
	requireBusyHolder(t, "TTW auto-swap during admission", err, "ttw", dto.BusyOperationLaunch, "ttw")
	if !d.mountMgrs["falloutnv"].IsMounted() {
		t.Fatal("FNV farm was torn down during TTW admission")
	}
	reservation.Release()
	for _, op := range []string{dto.BusyOperationTool, dto.BusyOperationScriptExtender} {
		other, err := d.acquireSharedOwned("ttw", op)
		if err != nil {
			t.Fatal(err)
		}
		requireBusyHolder(t, "FNV Apply during TTW "+op, d.RebuildVFS("falloutnv"), "falloutnv", op, "ttw")
		other.Release()
	}
	if err := d.UnmountVFS("falloutnv"); err != nil {
		t.Fatalf("unmount after TTW admission ends: %v", err)
	}
}
